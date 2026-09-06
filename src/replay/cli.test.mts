/**
 * The exit-code contract, offline. The driver is faked at the network edge; the
 * replay runner, evidence assembly, and classification underneath are real.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Page } from "playwright-core";
import type { Driver, DriverSession } from "../driver/types.mts";
import type { Spec } from "../spec/types.mts";
import type { StatusEvent } from "../status/stream.mts";
import { runReplayCli, type ReplayCliArgs } from "./cli.mts";
import { sessionReference } from "./evidence.mts";

const SPEC: Spec = {
  name: "one-goto",
  startUrl: "http://app.test/",
  steps: [{ id: "st_1", index: 1, action: "goto", target: "http://app.test/" }],
};

interface FakeBehaviour {
  open?: () => Promise<void>;
  goto?: () => Promise<void>;
  close?: () => Promise<void>;
  cdpConnect?: { latencyMs: number; retried: boolean };
}

function fakeDriver(behaviour: FakeBehaviour = {}): Driver {
  return {
    name: "fake",
    canRecord: false,
    async open(): Promise<DriverSession> {
      await behaviour.open?.();
      const page = {
        // The runner waits two animation frames after a goto (the paint signal).
        evaluate: async () => undefined,
        goto: async () => {
          await behaviour.goto?.();
        },
      } as unknown as Page;
      return {
        sessionId: "fake-1",
        page,
        cdpConnect: behaviour.cdpConnect,
        async fetchReplay() {
          return null;
        },
        close: behaviour.close ?? (async () => {}),
      };
    },
  };
}

function harness(overrides: Partial<ReplayCliArgs> = {}) {
  const out: string[] = [];
  const err: string[] = [];
  const args: ReplayCliArgs = {
    spec: SPEC,
    driver: fakeDriver(),
    io: { log: (l) => out.push(l), error: (l) => err.push(l) },
    writeEvidence: () => {},
    ...overrides,
  };
  return { args, out, err };
}

test("0 — the replay ran and passed", async () => {
  const { args, out } = harness();
  assert.equal(await runReplayCli(args), 0);
  assert.match(out.join("\n"), /PASSED/);
});

test("the written evidence record names the backing session", async () => {
  let written = "";
  const { args } = harness({
    evidenceFile: "evidence.json",
    writeEvidence: (_file, json) => {
      written = json;
    },
  });
  assert.equal(await runReplayCli(args), 0);
  const record = JSON.parse(written) as { sessionId?: string; driver: string };
  assert.equal(record.driver, "fake");
  assert.match(record.sessionId ?? "", /^session_[0-9a-f]{32}$/);
  assert.ok(!written.includes("fake-1"));
});

test("the written evidence record carries the session's cdpConnect diagnostics", async () => {
  let written = "";
  const { args } = harness({
    driver: fakeDriver({ cdpConnect: { latencyMs: 842, retried: true } }),
    evidenceFile: "evidence.json",
    writeEvidence: (_file, json) => {
      written = json;
    },
  });
  assert.equal(await runReplayCli(args), 0);
  const record = JSON.parse(written) as {
    cdpConnect?: { latencyMs: number; retried: boolean };
  };
  assert.deepEqual(record.cdpConnect, { latencyMs: 842, retried: true });
});

test("status progress ends with the evidence artifact on the terminal event, and that artifact really was written", async () => {
  const dir = mkdtempSync(join(tmpdir(), "formic-replay-status-"));
  try {
    const evidenceFile = join(dir, "evidence.json");
    const events: StatusEvent[] = [];
    const { args } = harness({
      evidenceFile,
      writeEvidence: writeFileSync,
      status: { emit: (event) => events.push(event) },
    });

    assert.equal(await runReplayCli(args), 0);
    assert.deepEqual(events, [
      { event: "progress" },
      { event: "ok", artifact: evidenceFile },
    ]);
    const record = JSON.parse(readFileSync(evidenceFile, "utf8")) as {
      sessionId?: string;
    };
    assert.equal(
      record.sessionId,
      sessionReference("fake-1"),
      "the artifact the terminal event names must be a real, populated file",
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("1 — the replay ran and a step failed", async () => {
  const { args, out } = harness({
    driver: fakeDriver({
      goto: async () => {
        throw new Error("net::ERR_CONNECTION_REFUSED");
      },
    }),
  });
  assert.equal(await runReplayCli(args), 1);
  assert.match(out.join("\n"), /FAILED[\s\S]*action phase/);
});

test("2 — the session could not open (never a test failure)", async () => {
  const { args, err } = harness({
    driver: fakeDriver({
      open: async () => {
        throw new Error("401 Unauthorized");
      },
    }),
  });
  assert.equal(await runReplayCli(args), 2);
  assert.match(err.join("\n"), /could not start: 401/);
});

test("2 — a PASSED replay whose evidence cannot be written", async () => {
  const { args, out, err } = harness({
    evidenceFile: "/no/such/dir/evidence.json",
    writeEvidence: () => {
      throw new Error("ENOENT");
    },
  });
  assert.equal(await runReplayCli(args), 2);
  assert.match(out.join("\n"), /PASSED/, "the verdict is still reported");
  assert.match(err.join("\n"), /evidence failed: ENOENT/);
});

test("REVIEW REGRESSION (P2) — 2 when the session cannot be closed after a passed run", async () => {
  // A close that keeps failing (a Solari release that will not complete) used to
  // escape every catch and exit 1 — reported as a failed test, and hiding that a
  // paid session may still be live.
  const { args, err } = harness({
    driver: fakeDriver({
      close: async () => {
        throw new Error("release timed out");
      },
    }),
  });
  assert.equal(await runReplayCli(args), 2);
  assert.match(err.join("\n"), /session close failed: release timed out/);
});

test("a close failure never upgrades a FAILED replay to 'passed' or hides it", async () => {
  const { args, out } = harness({
    driver: fakeDriver({
      goto: async () => {
        throw new Error("boom");
      },
      close: async () => {
        throw new Error("release timed out");
      },
    }),
  });
  assert.equal(await runReplayCli(args), 2);
  assert.match(out.join("\n"), /FAILED/, "the replay verdict is still printed");
});

test("a cleanup failure after a passed replay downgrades the exit code to 2 and the terminal event to failed", async () => {
  const events: StatusEvent[] = [];
  const { args, err } = harness({
    evidenceFile: "evidence.json",
    status: { emit: (event) => events.push(event) },
    cleanup: async () => {
      throw new Error("host close failed");
    },
  });

  assert.equal(await runReplayCli(args), 2);
  assert.match(err.join("\n"), /cleanup failed: host close failed/);
  assert.deepEqual(events, [
    { event: "progress" },
    { event: "failed", artifact: "evidence.json" },
  ]);
});

test("cleanup runs and its success never changes a passed verdict", async () => {
  const calls: string[] = [];
  const events: StatusEvent[] = [];
  const { args } = harness({
    status: { emit: (event) => events.push(event) },
    cleanup: async () => {
      calls.push("cleanup");
    },
  });

  assert.equal(await runReplayCli(args), 0);
  assert.deepEqual(calls, ["cleanup"]);
  assert.deepEqual(events, [{ event: "progress" }, { event: "ok" }]);
});

test("--app: printed step lines and the written record never carry the host's tokened URL; the audit host row stays redacted", async () => {
  const hosted: Spec = {
    ...SPEC,
    startUrl: "https://sbx.example/?pt_token=SECRET",
    steps: [
      {
        id: "st_1",
        index: 1,
        action: "goto",
        target: "https://sbx.example/?pt_token=SECRET",
      },
    ],
  };
  let written = "";
  const { args, out } = harness({
    spec: hosted,
    evidenceFile: "evidence.json",
    writeEvidence: (_file, json) => {
      written = json;
    },
    host: {
      name: "solari-sandbox",
      kind: "Outside",
      baseUrl: "https://sbx.example/?pt_token=<redacted>",
    },
    appHost: {
      baseUrl: "https://sbx.example/?pt_token=SECRET",
      originalOrigin: "http://app.test",
    },
  });
  assert.equal(await runReplayCli(args), 0);
  const printed = out.join("\n");
  assert.ok(!printed.includes("SECRET"), printed);
  assert.match(printed, /1 goto {3}http:\/\/app\.test\//);
  assert.ok(!written.includes("SECRET"));
  const record = JSON.parse(written) as {
    steps: { target?: string }[];
    host: { baseUrl: string };
  };
  assert.equal(record.steps[0].target, "http://app.test/");
  assert.equal(record.host.baseUrl, "https://sbx.example/?pt_token=<redacted>");
});

test("a secret too short to redact from free text is said out loud, once", async () => {
  // Below the floor a value is not substituted out of free text, because doing so
  // corrupts the record it protects (see SECRET_MIN_FREE_TEXT_LENGTH). That is a real
  // reduction in what the run can promise, and it is invisible unless it is said: the
  // step log still withholds the value structurally, but a Playwright error quoting the
  // field's contents would carry it into the terminal and the evidence.
  process.env.SHORT_PIN = "1234";
  const spec: Spec = {
    name: "short-secret",
    startUrl: "http://app.test/",
    steps: [
      { id: "st_1", index: 1, action: "goto", target: "http://app.test/" },
      {
        id: "st_2",
        index: 2,
        action: "fill",
        target: "#pin",
        valueFrom: "env.SHORT_PIN",
        assert: { selector: "#pin", visible: true },
      },
    ],
  };
  const { args, out } = harness({ spec });
  try {
    await runReplayCli(args);
  } finally {
    delete process.env.SHORT_PIN;
  }
  const warnings = out.filter((line) => line.includes("SHORT_PIN"));
  assert.equal(warnings.length, 1, out.join("\n"));
  assert.match(warnings[0], /too short to redact/);
});

test("an error printed after the run is scrubbed of what the run resolved", async () => {
  // The evidence stage runs after replay and prints whatever it throws. An error raised
  // while writing evidence for a step the run resolved from the environment is exactly
  // the one that would quote it — and the terminal is an artifact.
  const canary = "canary-secret-value";
  process.env.CLI_PRINT_PASSWORD = canary;
  const spec: Spec = {
    name: "printed-error",
    startUrl: "http://app.test/",
    steps: [
      { id: "st_1", index: 1, action: "goto", target: "http://app.test/" },
      {
        id: "st_2",
        index: 2,
        action: "fill",
        target: "#password",
        valueFrom: "env.CLI_PRINT_PASSWORD",
        assert: { selector: "#password", visible: true },
      },
    ],
  };
  const { args, err } = harness({
    spec,
    evidenceFile: "evidence.json",
    writeEvidence: () => {
      throw new Error(`disk full while writing "${canary}"`);
    },
  });
  try {
    assert.equal(await runReplayCli(args), 2);
  } finally {
    delete process.env.CLI_PRINT_PASSWORD;
  }
  const printed = err.join("\n");
  assert.equal(printed.includes(canary), false, printed);
  assert.match(printed, /disk full/);
  assert.match(printed, /redacted:env\.CLI_PRINT_PASSWORD/);
});

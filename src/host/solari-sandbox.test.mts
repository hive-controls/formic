/**
 * openSolariSandbox, offline: a fake SandboxClientLike stands in for the SDK so setup
 * order, upload byte-fidelity, readiness polling, and every failure-cleanup path are
 * testable with no key and no network — the pattern driver/solari.test.mts uses for the
 * live-session leak paths.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import {
  isMachineIdFormat,
  neutraliseGuestMachineId,
  openSolariSandbox,
  type GuestIdentityTarget,
  type SandboxClientLike,
  type SandboxCommandHandleLike,
  type SandboxLike,
} from "./solari-sandbox.mts";

interface FakeOptions {
  hasPython3?: boolean;
  pollStatuses?: string[];
  pollHangs?: boolean;
  uploadError?: Error;
  previewUrlError?: Error;
  killHangs?: boolean;
  serverWaitResolves?: number;
}

function fakeSandbox(opts: FakeOptions = {}) {
  const calls: string[] = [];
  const uploads: Array<{ path: string; data: Uint8Array | string }> = [];
  let pollIndex = 0;
  const sandbox: SandboxLike = {
    id: "sbx_test",
    async connect() {
      calls.push("connect");
    },
    files: {
      async mkdir(path) {
        calls.push(`mkdir:${path}`);
      },
      async upload(path, data) {
        calls.push(`upload:${path}`);
        uploads.push({ path, data });
        if (opts.uploadError) throw opts.uploadError;
      },
    },
    commands: {
      async run(cmd, runOpts) {
        const script = runOpts?.args?.[1] ?? "";
        if (script.includes("/etc/machine-id")) {
          calls.push("run:machine-id");
          const match = script.match(/'([0-9a-f]{32})'/);
          return {
            exitCode: 0,
            stdout: `${match?.[1] ?? "0".repeat(32)}\n`,
            stderr: "",
          };
        }
        if (script === "command -v python3") {
          calls.push("run:which-python3");
          return {
            exitCode: opts.hasPython3 === false ? 1 : 0,
            stdout: "",
            stderr: "",
          };
        }
        calls.push("run:poll");
        if (opts.pollHangs) return new Promise(() => {});
        const status = opts.pollStatuses?.[pollIndex] ?? "200";
        pollIndex++;
        return { exitCode: 0, stdout: status, stderr: "" };
      },
      async start(cmd): Promise<SandboxCommandHandleLike> {
        calls.push(`start:${cmd}`);
        return {
          async wait() {
            if (opts.serverWaitResolves !== undefined)
              return opts.serverWaitResolves;
            return new Promise(() => {});
          },
          async kill() {},
        };
      },
    },
    async previewUrl(port) {
      calls.push("previewUrl");
      if (opts.previewUrlError) throw opts.previewUrlError;
      return {
        url: `https://sbx-test-${port}.preview.example.com/?token=SECRET`,
      };
    },
    async kill() {
      calls.push("kill");
      if (opts.killHangs) return new Promise(() => {});
    },
  };
  return { sandbox, calls, uploads };
}

function fakeClient(
  sandbox: SandboxLike,
  createCalls: unknown[] = [],
): SandboxClientLike {
  return {
    async create(opts) {
      createCalls.push(opts);
      return sandbox;
    },
  };
}

let scratch: string;
function scratchApp(files: Record<string, Uint8Array | string>): string {
  scratch = mkdtempSync(join(tmpdir(), "solari-sandbox-test-"));
  for (const [name, data] of Object.entries(files)) {
    writeFileSync(join(scratch, name), data);
  }
  return scratch;
}
function cleanupScratch() {
  if (scratch) rmSync(scratch, { recursive: true, force: true });
}

test("exact call order: create, connect, mkdir, upload, python3 probe, start, poll, previewUrl", async () => {
  const appDir = scratchApp({ "index.html": "<html></html>" });
  const { sandbox, calls } = fakeSandbox({ pollStatuses: ["200"] });
  const createCalls: unknown[] = [];
  await openSolariSandbox(
    appDir,
    { apiKey: "k", client: fakeClient(sandbox, createCalls) },
    { log() {} },
  );
  cleanupScratch();
  assert.deepEqual(calls, [
    "connect",
    "run:machine-id",
    "mkdir:/tmp/app",
    "upload:/tmp/app/index.html",
    "run:which-python3",
    "start:python3",
    "run:poll",
    "previewUrl",
  ]);
});

test("uploaded bytes are a Uint8Array, and a 0xFF byte fixture round-trips exactly", async () => {
  const fixture = Uint8Array.from([0x00, 0xff, 0x10, 0xfe]);
  const appDir = scratchApp({ "binary.dat": fixture });
  const { sandbox, uploads } = fakeSandbox();
  await openSolariSandbox(appDir, { apiKey: "k", client: fakeClient(sandbox) });
  cleanupScratch();
  assert.equal(uploads.length, 1);
  assert.ok(uploads[0].data instanceof Uint8Array);
  assert.deepEqual(Uint8Array.from(uploads[0].data as Uint8Array), fixture);
});

test("baseUrl equals the fake's tokened previewUrl verbatim, and name is solari-sandbox", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const { sandbox } = fakeSandbox();
  const app = await openSolariSandbox(appDir, {
    apiKey: "k",
    port: 4173,
    client: fakeClient(sandbox),
  });
  cleanupScratch();
  assert.equal(app.name, "solari-sandbox");
  assert.equal(
    app.baseUrl,
    "https://sbx-test-4173.preview.example.com/?token=SECRET",
  );
  assert.equal(app.guestIdentity?.sandboxId, "sbx_test");
  assert.equal(isMachineIdFormat(app.guestIdentity?.machineId ?? ""), true);
});

test("metadata and the 30-minute timeout reach create()", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const { sandbox } = fakeSandbox();
  const createCalls: Array<{
    metadata?: Record<string, string>;
    timeoutMs?: number;
  }> = [];
  await openSolariSandbox(appDir, {
    apiKey: "k",
    client: fakeClient(sandbox, createCalls),
  });
  cleanupScratch();
  assert.equal(createCalls.length, 1);
  assert.equal(createCalls[0].metadata?.tool, "e2e-doctor");
  assert.equal(createCalls[0].metadata?.app, basename(appDir));
  assert.equal(createCalls[0].timeoutMs, 30 * 60 * 1000);
});

test("readiness polling: 000, 000, 200 takes exactly 3 polls and no local sleeps", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const { sandbox, calls } = fakeSandbox({
    pollStatuses: ["000", "000", "200"],
  });
  const started = Date.now();
  await openSolariSandbox(appDir, { apiKey: "k", client: fakeClient(sandbox) });
  const elapsedMs = Date.now() - started;
  cleanupScratch();
  assert.equal(calls.filter((c) => c === "run:poll").length, 3);
  assert.ok(
    elapsedMs < 500,
    `expected no local sleeps between polls, took ${elapsedMs}ms`,
  );
});

test("a server that never returns 200 rejects, and the sandbox is killed exactly once", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const { sandbox, calls } = fakeSandbox({
    pollStatuses: ["000", "000", "000"],
  });
  await assert.rejects(
    openSolariSandbox(appDir, {
      apiKey: "k",
      readyTimeoutMs: 1500,
      client: fakeClient(sandbox),
    }),
    /never became ready/,
  );
  cleanupScratch();
  assert.equal(calls.filter((c) => c === "kill").length, 1);
});

test("a server that exits before it is ready fails fast, not after the full poll budget", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const { sandbox, calls } = fakeSandbox({
    pollHangs: true,
    serverWaitResolves: 1,
  });
  await assert.rejects(
    openSolariSandbox(appDir, { apiKey: "k", client: fakeClient(sandbox) }),
    /exited \(code 1\) before it became ready/,
  );
  cleanupScratch();
  assert.equal(calls.filter((c) => c === "kill").length, 1);
});

test("an upload failure kills the sandbox once and rethrows the exact same error", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const uploadError = new Error("upload refused");
  const { sandbox, calls } = fakeSandbox({ uploadError });
  await assert.rejects(
    openSolariSandbox(appDir, { apiKey: "k", client: fakeClient(sandbox) }),
    (err: unknown) => err === uploadError,
  );
  cleanupScratch();
  assert.equal(calls.filter((c) => c === "kill").length, 1);
});

test("a previewUrl failure kills the sandbox once and rethrows the exact same error", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const previewUrlError = new Error("previewUrl refused");
  const { sandbox, calls } = fakeSandbox({ previewUrlError });
  await assert.rejects(
    openSolariSandbox(appDir, { apiKey: "k", client: fakeClient(sandbox) }),
    (err: unknown) => err === previewUrlError,
  );
  cleanupScratch();
  assert.equal(calls.filter((c) => c === "kill").length, 1);
});

test("a hanging kill still lets close() resolve, capped by killTimeoutMs", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const { sandbox } = fakeSandbox({ killHangs: true });
  const app = await openSolariSandbox(appDir, {
    apiKey: "k",
    killTimeoutMs: 20,
    client: fakeClient(sandbox),
  });
  const started = Date.now();
  await app.close();
  const elapsedMs = Date.now() - started;
  cleanupScratch();
  assert.ok(
    elapsedMs < 1000,
    `close() should resolve near killTimeoutMs, took ${elapsedMs}ms`,
  );
});

test("no python3 in the guest falls back to the node static server", async () => {
  const appDir = scratchApp({ "index.html": "x" });
  const { sandbox, calls } = fakeSandbox({ hasPython3: false });
  await openSolariSandbox(appDir, { apiKey: "k", client: fakeClient(sandbox) });
  cleanupScratch();
  assert.ok(calls.includes("start:node"));
  assert.ok(!calls.includes("start:python3"));
});

const BAKED_MACHINE_ID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";

function identityTarget(opts: {
  write?: (id: string) => { exitCode: number; stdout: string; stderr: string };
}): GuestIdentityTarget {
  return {
    id: "sbx_same",
    commands: {
      async run(_cmd, runOpts) {
        const script = runOpts?.args?.[1] ?? "";
        const match = script.match(/'([0-9a-f]{32})'/);
        if (!match) {
          return { exitCode: 0, stdout: `${BAKED_MACHINE_ID}\n`, stderr: "" };
        }
        if (opts.write) return opts.write(match[1]);
        return { exitCode: 0, stdout: `${match[1]}\n`, stderr: "" };
      },
      async start() {
        throw new Error("start is not used by identity");
      },
    },
  };
}

test("isMachineIdFormat accepts only a 32-char lowercase hex id", () => {
  assert.equal(isMachineIdFormat("0123456789abcdef0123456789abcdef"), true);
  assert.equal(isMachineIdFormat("0123456789ABCDEF0123456789ABCDEF"), false);
  assert.equal(
    isMachineIdFormat("01234567-89ab-cdef-0123-456789abcdef"),
    false,
  );
  assert.equal(isMachineIdFormat(""), false);
  assert.equal(isMachineIdFormat("0123456789abcdef0123456789abcde"), false);
  assert.equal(isMachineIdFormat("0123456789abcdef0123456789abcdef0"), false);
  assert.equal(isMachineIdFormat("0123456789abcdef0123456789abcdef\n"), false);
});

test("two consecutive sessions with the same sandbox id report different machine ids", async () => {
  const sandbox = identityTarget({});
  const first = await neutraliseGuestMachineId(sandbox);
  const second = await neutraliseGuestMachineId(sandbox);
  assert.equal(first.sandboxId, "sbx_same");
  assert.equal(second.sandboxId, first.sandboxId);
  assert.notEqual(first.machineId, second.machineId);
  assert.equal(isMachineIdFormat(first.machineId), true);
  assert.equal(isMachineIdFormat(second.machineId), true);
  assert.notEqual(first.machineId, BAKED_MACHINE_ID);
  assert.notEqual(second.machineId, BAKED_MACHINE_ID);
});

test("a refused /etc/machine-id write throws with the guest exit code", async () => {
  const sandbox = identityTarget({
    write: () => ({
      exitCode: 1,
      stdout: "",
      stderr: "Read-only file system",
    }),
  });
  await assert.rejects(
    () => neutraliseGuestMachineId(sandbox),
    /could not write \/etc\/machine-id \(exit 1\): Read-only file system/,
  );
});

const liveRequested = process.env.FORMIC_LIVE_SANDBOX === "1";
const liveKeyPresent = Boolean(process.env.SOLARI_API_KEY);
test(
  "two live sandboxes report different machine ids",
  {
    timeout: 120_000,
    skip:
      liveRequested && liveKeyPresent
        ? false
        : "SOLARI live sandbox not requested (FORMIC_LIVE_SANDBOX!=1) or SOLARI_API_KEY unset",
  },
  async () => {
    const { SandboxClient } = await import("@solarisdk/sandbox");
    const client = new SandboxClient({
      apiKey: process.env.SOLARI_API_KEY as string,
      baseUrl: process.env.SOLARI_BASE_URL ?? "https://api.getsolari.com",
    });
    const seen: string[] = [];
    for (let i = 0; i < 2; i++) {
      const sandbox = await client.create({
        metadata: { tool: "guest-identity" },
        timeoutMs: 5 * 60 * 1000,
      });
      try {
        await sandbox.connect();
        const before = await sandbox.commands.run("sh", {
          args: ["-c", "cat /etc/machine-id"],
        });
        const identity = await neutraliseGuestMachineId(sandbox);
        const after = await sandbox.commands.run("sh", {
          args: ["-c", "cat /etc/machine-id"],
        });
        console.log(
          JSON.stringify({
            round: i + 1,
            sandboxId: sandbox.id,
            beforeExit: before.exitCode,
            beforeId: before.stdout.trim(),
            writtenId: identity.machineId,
            afterExit: after.exitCode,
            afterId: after.stdout.trim(),
          }),
        );
        assert.equal(before.exitCode, 0);
        assert.equal(after.exitCode, 0);
        assert.equal(after.stdout.trim(), identity.machineId);
        assert.equal(isMachineIdFormat(identity.machineId), true);
        seen.push(identity.machineId);
      } finally {
        await sandbox.kill().catch(() => {});
      }
    }
    assert.equal(seen.length, 2);
    assert.notEqual(seen[0], seen[1]);
  },
);

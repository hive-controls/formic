/**
 * The reporter's decision of WHEN to heal and what it hands the CLI, offline: the
 * subprocess spawn is faked at the process edge (RunHeal), same shape as
 * heal/pr.mts's injectable CommandRunner. TestCase and TestResult are the runner's
 * own value types — data the reporter reads, not an edge it talks to.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import type { TestCase, TestResult } from "@playwright/test/reporter";
import HealReporter, { SPEC_FILE_ANNOTATION } from "./reporter.mts";

interface FakeTestOptions {
  specFile?: string;
  /** What Playwright concluded about the whole test, across its attempts. */
  outcome?: "expected" | "unexpected" | "flaky" | "skipped";
  /** How many retries the project allows — an attempt below this is not final. */
  retries?: number;
}

function fakeTest(options: FakeTestOptions = {}): TestCase {
  const { specFile, outcome = "unexpected", retries = 0 } = options;
  return {
    retries,
    outcome: () => outcome,
    annotations: specFile
      ? [{ type: SPEC_FILE_ANNOTATION, description: specFile }]
      : [],
  } as unknown as TestCase;
}

function fakeResult(
  status: "failed" | "timedOut" | "passed" = "failed",
  retry = 0,
): TestResult {
  return { status, retry } as unknown as TestResult;
}

/** Captures a console method for the duration of `body`, restoring it after. */
async function captureConsole(
  method: "log" | "error",
  body: () => Promise<void>,
): Promise<string[]> {
  const lines: string[] = [];
  const original = console[method];
  console[method] = (...args: unknown[]) => {
    lines.push(args.map(String).join(" "));
  };
  try {
    await body();
  } finally {
    console[method] = original;
  }
  return lines;
}

test("heal disabled by default — never spawns even on a failed spec test", async () => {
  let calls = 0;
  const reporter = new HealReporter({
    runHeal: async () => {
      calls++;
      return { stdout: "" };
    },
  });
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult(),
  );
  assert.equal(calls, 0);
});

test("heal enabled, a passed test — never spawns", async () => {
  let calls = 0;
  const reporter = new HealReporter({
    heal: true,
    runHeal: async () => {
      calls++;
      return { stdout: "" };
    },
  });
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml", outcome: "expected" }),
    fakeResult("passed"),
  );
  assert.equal(calls, 0);
});

test("heal enabled, a failed test with no spec annotation — never spawns (not a spec-backed test)", async () => {
  let calls = 0;
  const reporter = new HealReporter({
    heal: true,
    runHeal: async () => {
      calls++;
      return { stdout: "" };
    },
  });
  await reporter.onTestEnd(fakeTest(), fakeResult());
  assert.equal(calls, 0);
});

test("an attempt Playwright will still retry is not the final outcome — never spawns", async () => {
  let calls = 0;
  const reporter = new HealReporter({
    heal: true,
    runHeal: async () => {
      calls++;
      return { stdout: "" };
    },
  });
  // Attempt 0 of a project allowing 2 retries: two more attempts are still to come.
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml", retries: 2 }),
    fakeResult("failed", 0),
  );
  assert.equal(calls, 0, "a retryable attempt must not spend a healer call");

  // The last attempt of the same test IS final.
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml", retries: 2 }),
    fakeResult("failed", 2),
  );
  assert.equal(calls, 1);
});

test("a timed-out final attempt heals just like a failed one", async () => {
  // fakeResult's status type has carried "timedOut" since it was written, but no
  // test ever passed it — the reporter's `status !== "failed" && status !==
  // "timedOut"` guard's second half was accepted, never exercised.
  let calls = 0;
  const reporter = new HealReporter({
    heal: true,
    runHeal: async () => {
      calls++;
      return { stdout: "" };
    },
  });
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult("timedOut"),
  );
  assert.equal(
    calls,
    1,
    "a timed-out attempt is a genuine failure, not skipped",
  );
});

test("a test that a later attempt passed is flaky, not broken — never spawns", async () => {
  let calls = 0;
  const reporter = new HealReporter({
    heal: true,
    runHeal: async () => {
      calls++;
      return { stdout: "" };
    },
  });
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml", outcome: "flaky", retries: 1 }),
    fakeResult("failed", 1),
  );
  assert.equal(calls, 0);
});

test("a test expected to fail that PASSES is unexpected but not broken — never heals, never spends its dedupe slot", async () => {
  const seen: string[][] = [];
  const reporter = new HealReporter({
    heal: true,
    runHeal: async (args) => {
      seen.push(args);
      return { stdout: "" };
    },
  });
  // `test.fail()` annotated, and it passed: status "passed", outcome "unexpected".
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult("passed"),
  );
  assert.equal(seen.length, 0, "a passing test is never healed");

  // And the dedupe slot must still be free: a GENUINE failure of the same spec
  // later in the run must still heal.
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult("failed"),
  );
  assert.equal(seen.length, 1, "the real failure still heals");
  assert.deepEqual(seen[0].slice(-2), ["heal", "/specs/a.yaml"]);
});

test("one spec failing twice in a run heals once — one healer call, at most one PR", async () => {
  const seen: string[][] = [];
  const reporter = new HealReporter({
    heal: true,
    pr: true,
    runHeal: async (args) => {
      seen.push(args);
      return { stdout: "" };
    },
  });
  // The same spec file, failed by two tests (two projects, say).
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult(),
  );
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult(),
  );
  // A different spec is a different repair and still heals.
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/b.yaml" }),
    fakeResult(),
  );
  assert.equal(seen.length, 2);
  assert.deepEqual(
    seen.map((args) => args[args.length - 2]),
    ["/specs/a.yaml", "/specs/b.yaml"],
  );
});

test("heal enabled, a failed spec test — spawns heal on the spec file, no --pr by default", async () => {
  let seenArgs: string[] = [];
  const reporter = new HealReporter({
    heal: true,
    runHeal: async (args) => {
      seenArgs = args;
      return { stdout: "healed" };
    },
  });
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult(),
  );
  assert.deepEqual(seenArgs.slice(-2), ["heal", "/specs/a.yaml"]);
  assert.ok(!seenArgs.includes("--pr"));
});

test("heal + pr enabled — --pr reaches the CLI args, reusing its own PR path", async () => {
  let seenArgs: string[] = [];
  const reporter = new HealReporter({
    heal: true,
    pr: true,
    runHeal: async (args) => {
      seenArgs = args;
      return { stdout: "" };
    },
  });
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult(),
  );
  assert.ok(seenArgs.includes("--pr"));
});

test("the app option reaches the CLI as --app, so heal targets the same application", async () => {
  let seenArgs: string[] = [];
  const reporter = new HealReporter({
    heal: true,
    app: "/app/sample-app",
    runHeal: async (args) => {
      seenArgs = args;
      return { stdout: "" };
    },
  });
  await reporter.onTestEnd(
    fakeTest({ specFile: "/specs/a.yaml" }),
    fakeResult(),
  );
  const appFlag = seenArgs.indexOf("--app");
  assert.notEqual(appFlag, -1, "--app is forwarded");
  assert.equal(seenArgs[appFlag + 1], "/app/sample-app");
});

test("the env option is merged OVER process.env — the option wins on the same key, the rest is inherited", async () => {
  let seenEnv: NodeJS.ProcessEnv = {};
  // The SAME key in both places with different values: only a merge in the right
  // order can produce the option's value here.
  process.env.FORMIC_HEALER = "ambient-healer";
  process.env.FORMIC_REPORTER_PROBE = "inherited";
  const reporter = new HealReporter({
    heal: true,
    env: { FORMIC_HEALER: "option-healer" },
    runHeal: async (_args, env) => {
      seenEnv = env;
      return { stdout: "" };
    },
  });
  try {
    await reporter.onTestEnd(
      fakeTest({ specFile: "/specs/a.yaml" }),
      fakeResult(),
    );
  } finally {
    delete process.env.FORMIC_HEALER;
    delete process.env.FORMIC_REPORTER_PROBE;
  }
  assert.equal(
    seenEnv.FORMIC_HEALER,
    "option-healer",
    "the configured env overrides the ambient value of the same key",
  );
  assert.equal(
    seenEnv.FORMIC_REPORTER_PROBE,
    "inherited",
    "a key the option does not name (FORMIC_GATE included) still reaches the CLI",
  );
});

test("a spawn failure is logged with the healer's stderr, never thrown out of onTestEnd", async () => {
  const reporter = new HealReporter({
    heal: true,
    runHeal: async () => {
      throw Object.assign(new Error("boom"), { stderr: "healer crashed" });
    },
  });
  const logged = await captureConsole("error", async () => {
    await reporter.onTestEnd(
      fakeTest({ specFile: "/specs/a.yaml" }),
      fakeResult(),
    );
  });
  assert.deepEqual(logged, ["heal reporter: healer crashed"]);
});

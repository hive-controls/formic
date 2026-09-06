/**
 * The `record` command's contract, with the browser faked at the binding — the one
 * edge a unit test should mock. Everything above it (the pump, the step log, the
 * proposal derivation, the validator) is the real code.
 *
 * What is pinned here is the exit-code meaning, because that is what a human and a CI
 * job route on: a spec written, a human's decision that left nothing to write, and a
 * harness failure are three different outcomes and must never collapse into one.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import type { Page } from "playwright-core";
import type { Driver, DriverSession } from "../driver/types.mts";
import { CAPTURE_BINDING, type CapturedEvent } from "../capture/events.mts";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { loadSpec } from "../spec/parse.mts";
import { acceptsProposal, runRecordCli, writeSpecFile } from "./record-cli.mts";

const START_URL = "http://app.test/";

const APPROVE_CLICK: CapturedEvent = {
  kind: "click",
  frameId: "f_fake",
  seq: 1,
  timestamp: Date.now(),
  url: START_URL,
  state: { url: START_URL, nodes: [] },
  target: {
    selector: "#approve-button",
    role: "button",
    name: "Approve order",
  },
  tagName: "button",
};

/** The page the bundle would have been injected into. `evaluate` answers the two things
 *  the recorder asks a real page for: its clock, and its final visible state. */
function fakePage(hold: (emit: (event: CapturedEvent) => void) => void): Page {
  // Events arrive stamped with the frame they came from — the recorder refuses anything
  // that is not the main one — so the fake has to name a main frame and send from it.
  const mainFrame = { url: () => START_URL };
  return {
    mainFrame: () => mainFrame,
    context: () => ({ addInitScript: async () => {} }),
    frames: () => [mainFrame],
    on: () => {},
    off: () => {},
    // The recording registers two bindings: the way events come out, and the question
    // "is a recording still running" that keeps a stopped one from re-arming itself on
    // the next document. Only the first carries events.
    exposeBinding: async (
      name: string,
      fn: (source: unknown, json: string) => void,
    ) => {
      if (name !== CAPTURE_BINDING) return;
      hold((event) => fn({ frame: mainFrame }, JSON.stringify(event)));
    },
    addInitScript: async () => {},
    goto: async () => {},
    evaluate: async (fn: unknown, arg?: unknown) => {
      const source = String(fn);
      if (/Date\.now\(\)/.test(source) && source.length < 60) return Date.now();
      // The drain asks for the document mark in ONE call; answer both halves together.
      if (Array.isArray(arg)) return { frameId: "f_fake", seq: 0 };
      return {
        url: START_URL,
        nodes: [{ testId: "confirmation", text: "Order approved" }],
      };
    },
    url: () => START_URL,
  } as unknown as Page;
}

interface Harness {
  driver: Driver;
  /** Resolves once the bundle's binding is installed and events can be delivered. */
  ready: Promise<(event: CapturedEvent) => void>;
  closed: () => boolean;
}

function fakeDriver(closeError?: string): Harness {
  let wasClosed = false;
  let announce: (emit: (event: CapturedEvent) => void) => void = () => {};
  const ready = new Promise<(event: CapturedEvent) => void>((resolve) => {
    announce = resolve;
  });
  const session: DriverSession = {
    sessionId: "fake",
    page: fakePage((emit) => announce(emit)),
    fetchReplay: async () => [],
    close: async () => {
      wasClosed = true;
      if (closeError !== undefined) throw new Error(closeError);
    },
  };
  return {
    driver: {
      name: "fake-driver",
      canRecord: true,
      open: async () => session,
    },
    ready,
    closed: () => wasClosed,
  };
}

interface Run {
  code: number;
  written: { file: string; yaml: string }[];
  log: string[];
  errors: string[];
}

async function runRecording(
  harness: Harness,
  confirm: () => Promise<boolean>,
  events: CapturedEvent[],
  cancelStop?: () => void,
  extra: { assumeYes?: boolean; includeSecrets?: boolean } = {},
): Promise<Run> {
  const written: { file: string; yaml: string }[] = [];
  const log: string[] = [];
  const errors: string[] = [];
  let stopNow = () => {};
  const stop = new Promise<void>((resolve) => {
    stopNow = resolve;
  });
  const running = runRecordCli({
    specName: "recorded",
    startUrl: START_URL,
    driver: harness.driver,
    outFile: "specs/recorded.yaml",
    stop,
    cancelStop,
    confirm,
    assumeYes: extra.assumeYes,
    includeSecrets: extra.includeSecrets,
    io: { log: (line) => log.push(line), error: (line) => errors.push(line) },
    writeSpec: (file, yaml) => written.push({ file, yaml }),
  });
  const emit = await harness.ready;
  for (const event of events) emit(event);
  // Let the pump drain what was emitted before the stop closes the last window.
  await new Promise((resolve) => setTimeout(resolve, 20));
  stopNow();
  return { code: await running, written, log, errors };
}

test("an accepted proposal is written into the spec the recording produced", async () => {
  const harness = fakeDriver();
  const run = await runRecording(harness, async () => true, [APPROVE_CLICK]);
  assert.equal(run.code, 0, run.errors.join("\n"));
  assert.equal(run.written.length, 1);
  assert.equal(run.written[0].file, "specs/recorded.yaml");

  const spec = loadSpec(run.written[0].yaml);
  assert.deepEqual(
    spec.steps.map((step) => [step.action, step.target]),
    [
      ["goto", START_URL],
      ["click", "#approve-button"],
    ],
  );
  assert.deepEqual(spec.steps[1].assert, {
    testId: "confirmation",
    hasText: "Order approved",
  });
  assert.ok(
    run.log.some((line) => line.includes("1 of 1 proposal(s) accepted")),
    "the confirmation pass must report what it adopted",
  );
  assert.ok(harness.closed(), "the session must be closed either way");
});

test("a DECLINED proposal leaves the step bare — reported as a human decision, not an error", async () => {
  const harness = fakeDriver();
  const run = await runRecording(harness, async () => false, [APPROVE_CLICK]);
  assert.equal(run.code, 1);
  assert.equal(run.written.length, 0, "an invalid spec must never be written");
  assert.ok(
    run.errors.some((line) => /declined proposal/.test(line)),
    `expected a declined-proposal refusal, got: ${run.errors.join("\n")}`,
  );
});

test("a recording with no actions writes nothing and says so", async () => {
  const harness = fakeDriver();
  const run = await runRecording(harness, async () => true, []);
  assert.equal(run.code, 1);
  assert.equal(run.written.length, 0);
  assert.ok(run.errors.some((line) => line.includes("nothing recorded")));
});

test("a session that will not open is a harness failure, exit 2", async () => {
  const log: string[] = [];
  const errors: string[] = [];
  const code = await runRecordCli({
    specName: "recorded",
    startUrl: START_URL,
    driver: {
      name: "fake-driver",
      canRecord: true,
      open: async () => {
        throw new Error("no browser here");
      },
    },
    outFile: "specs/recorded.yaml",
    stop: Promise.resolve(),
    confirm: async () => true,
    io: { log: (line) => log.push(line), error: (line) => errors.push(line) },
    writeSpec: () => {
      throw new Error("must not write");
    },
  });
  assert.equal(code, 2);
  assert.ok(errors.some((line) => line.includes("could not start")));
});

test("a session that will not CLOSE changes the exit code, whatever the recording did", async () => {
  // The early `return 1`/`return 0` fixed the code before `finally` ran, so a session
  // that may still be billing was reported in the log and then contradicted by the code
  // the caller routes on. Both outcomes must now end at 2.
  const written = await runRecording(
    fakeDriver("browser is wedged"),
    async () => true,
    [APPROVE_CLICK],
  );
  assert.equal(
    written.code,
    2,
    "a written spec does not excuse a leaked session",
  );
  assert.ok(written.errors.some((line) => /session close failed/.test(line)));

  const declined = await runRecording(
    fakeDriver("browser is wedged"),
    async () => false,
    [APPROVE_CLICK],
  );
  assert.equal(declined.code, 2);

  const empty = await runRecording(
    fakeDriver("browser is wedged"),
    async () => true,
    [],
  );
  assert.equal(empty.code, 2);
});

test("the wait for a key press is given up on EVERY exit path", async () => {
  let cancelled = 0;
  const finished = await runRecording(
    fakeDriver(),
    async () => true,
    [APPROVE_CLICK],
    () => {
      cancelled += 1;
    },
  );
  assert.equal(finished.code, 0);
  assert.equal(cancelled, 1, "a finished run must release the terminal");

  // A gate that never opens starts the wait and then returns before the browser exists.
  await runRecordCli({
    specName: "recorded",
    startUrl: START_URL,
    driver: {
      name: "fake-driver",
      canRecord: true,
      open: async () => {
        throw new Error("no browser here");
      },
    },
    outFile: "specs/recorded.yaml",
    stop: new Promise<void>(() => {}),
    cancelStop: () => {
      cancelled += 1;
    },
    confirm: async () => true,
    io: { log: () => {}, error: () => {} },
    writeSpec: () => {},
  });
  assert.equal(cancelled, 2, "a run that never started must release it too");
});

test("a withheld secret is named once, and the summary line says what replay needs", async () => {
  const harness = fakeDriver();
  const run = await runRecording(harness, async () => true, [
    {
      kind: "input",
      frameId: "f_fake",
      seq: 2,
      timestamp: Date.now(),
      url: START_URL,
      state: { url: START_URL, nodes: [] },
      target: { selector: "#password", role: "textbox", name: "Password" },
      tagName: "input",
      control: "text",
      classification: {
        category: "password",
        source: "type",
        evidence: "password",
      },
    },
  ]);
  assert.equal(run.code, 0, run.errors.join("\n"));
  const warning = run.log.find((line) => line.includes("warning:"));
  assert.ok(warning, `expected a secret warning, got: ${run.log.join("\n")}`);
  assert.match(warning, /#password/);
  assert.match(warning, /password data/);
  assert.match(warning, /env\.RECORDED_PASSWORD/);
  // ONE line at the end listing every variable — the per-step warnings scroll past in a
  // real recording, and a first replay that fails on an unmentioned variable is exactly
  // the confusion this line exists to prevent.
  const summary = run.log.find((line) =>
    line.includes("replay needs these environment variables:"),
  );
  assert.ok(summary, `expected a summary line, got: ${run.log.join("\n")}`);
  assert.match(summary, /RECORDED_PASSWORD/);
  const spec = loadSpec(run.written[0].yaml);
  assert.equal(spec.steps[1].valueFrom, "env.RECORDED_PASSWORD");
  assert.equal(spec.steps[1].value, undefined);
});

test("only y or yes adopts a proposal — a typo is not consent", async () => {
  for (const yes of ["y", "Y", "yes", "YES", " yes "]) {
    assert.equal(acceptsProposal(yes), true, `${yes} must adopt`);
  }
  for (const no of ["", "n", "no", "maybe", "yeah", "sure", "1", "yep"]) {
    assert.equal(acceptsProposal(no), false, `${no} must NOT adopt`);
  }
});

test("writing the default out path creates the directory that does not exist yet", async () => {
  // `specs/<name>.yaml` is where a FIRST recording goes, and a first recording is
  // exactly when the project has no `specs/` — so the command failed on the case it
  // exists for.
  const root = mkdtempSync(join(tmpdir(), "formic-record-out-"));
  const file = join(root, "specs", "recorded.yaml");
  writeSpecFile(file, "name: recorded\n");
  assert.equal(readFileSync(file, "utf8"), "name: recorded\n");
});

test("a navigation-only recording is a spec, not 'nothing recorded'", async () => {
  // `commitSpec` equated zero PROPOSALS with zero actions. A goto has no proposal by
  // design — the grammar exempts it, and proposing an expectation for a navigation the
  // human typed would ask them to confirm what they just did — so a recording that was
  // only navigation refused to write the perfectly valid spec it had produced.
  const harness = fakeDriver();
  const run = await runRecording(harness, async () => true, [
    // The page the recorder opened announces itself first, exactly as a real one does;
    // the recorder consumes that one as its own goto.
    {
      kind: "navigation",
      frameId: "f_fake",
      seq: 1,
      timestamp: Date.now(),
      url: START_URL,
      state: { url: START_URL, nodes: [] },
    },
    {
      kind: "navigation",
      frameId: "f_fake",
      seq: 2,
      timestamp: Date.now(),
      url: "http://app.test/orders",
      state: { url: "http://app.test/orders", nodes: [] },
    },
  ]);
  assert.equal(run.code, 0, run.errors.join("\n"));
  assert.equal(run.written.length, 1, "the spec must be written");
  const spec = loadSpec(run.written[0].yaml);
  assert.deepEqual(
    spec.steps.map((step) => step.action),
    ["goto", "goto"],
    "the opening navigation and the one the human made are both steps",
  );
});

test("a recording that could not represent something says so, once, out loud", async () => {
  // A refusal that only the recorder knows about is a spec silently missing an action.
  const harness = fakeDriver();
  const run = await runRecording(harness, async () => true, [
    {
      kind: "click",
      frameId: "f_fake",
      seq: 2,
      timestamp: Date.now(),
      url: START_URL,
      state: { url: START_URL, nodes: [] },
      target: {},
      tagName: "button",
    },
  ]);
  assert.ok(
    run.log.some((line) => /could not be addressed/.test(line)),
    `the refusal must reach the human — saw ${JSON.stringify(run.log)}`,
  );
});

/** A click whose consequence is the page rendering the signed-in user's own email —
 *  personal data the recorder never typed and cannot withhold at the value channel. */
function emailEchoEvents(
  actedStillVisible = false,
  afterUrl = START_URL,
): CapturedEvent[] {
  const state = (
    nodes: { testId?: string; text: string }[],
    acted?: boolean,
  ) => ({
    url: START_URL,
    nodes,
    ...(acted === undefined ? {} : { actedStillVisible: acted }),
  });
  return [
    {
      kind: "click",
      frameId: "f_fake",
      seq: 1,
      timestamp: Date.now(),
      url: START_URL,
      state: state([]),
      target: { selector: "#signin-button" },
      tagName: "button",
    },
    {
      kind: "click",
      frameId: "f_fake",
      seq: 2,
      timestamp: Date.now(),
      url: START_URL,
      // This state is the sign-in step's AFTER: the page says whether the button it
      // acted on is still there. A real sign-in swaps the form away, so it is not.
      state: {
        ...state(
          [{ testId: "current-user", text: "ops@example.test" }],
          actedStillVisible,
        ),
        url: afterUrl,
      },
      target: { selector: "#approve-button" },
      tagName: "button",
    },
  ];
}

test("--yes never adopts a proposal that quotes personal data back", async () => {
  // The recorder withholds what the human TYPES, but an application that renders the
  // signed-in user's own address puts it back on screen — and the proposal built from
  // that screen would commit it as `hasText`. Interactively a human sees the line and
  // can decline; `--yes` answers for them, so under `--yes` the proposal is dropped and
  // named instead of adopted silently.
  const run = await runRecording(
    fakeDriver(),
    async () => true,
    emailEchoEvents(),
    undefined,
    { assumeYes: true },
  );
  const warning = run.log.find((line) => /looks like email data/.test(line));
  assert.ok(warning, `the drop must be named — saw ${JSON.stringify(run.log)}`);
  assert.match(
    warning,
    /ops@example\.test/,
    "and must quote the proposal it dropped",
  );
  const everything = [
    ...run.log,
    ...run.errors,
    ...run.written.map((one) => one.yaml),
  ];
  assert.ok(
    !everything.some((line) => /hasText: ops@example\.test/.test(line)),
    "the address must not reach a written spec",
  );
});

test("a dropped proposal falls back to what the PAGE saw: the target went away", async () => {
  // Dropping the only candidate left a state-changing step with nothing to assert and
  // the validator refused the spec, so protecting the address cost the whole recording.
  // The stand-in is the step's own proven target — but asserting it is VISIBLE is false
  // exactly where it is needed, because a state-changing click usually removes the thing
  // it clicked. The page reports which happened, and the polarity follows.
  const run = await runRecording(
    fakeDriver(),
    async () => true,
    emailEchoEvents(false),
    undefined,
    { assumeYes: true },
  );

  assert.equal(run.code, 0, `--yes must still write: ${run.errors.join("\n")}`);
  const warning = run.log.find((line) => /looks like email data/.test(line));
  assert.ok(
    warning,
    `the drop is still named — saw ${JSON.stringify(run.log)}`,
  );
  assert.match(
    warning,
    /fell back to asserting the target is gone/,
    "and says WHICH fallback it used",
  );

  const written = run.written.at(-1);
  assert.ok(written, "a spec must be written");
  assert.ok(
    !/ops@example\.test/.test(written.yaml),
    `no address may reach the spec — saw ${written.yaml}`,
  );
  assert.match(
    written.yaml,
    /action: click\n\s+target: "#signin-button"\n\s+assert:\n\s+selector: "#signin-button"\n\s+visible: false/,
    `the sign-in step asserts its own target is GONE — saw ${written.yaml}`,
  );
});

test("a dropped proposal on a step that changed nothing asserts the target is still there", async () => {
  const run = await runRecording(
    fakeDriver(),
    async () => true,
    emailEchoEvents(true),
    undefined,
    { assumeYes: true },
  );

  assert.equal(run.code, 0, run.errors.join("\n"));
  const warning = run.log.find((line) => /looks like email data/.test(line));
  assert.ok(warning);
  assert.match(warning, /fell back to asserting the target is visible/);
  const written = run.written.at(-1);
  assert.ok(written);
  assert.match(
    written.yaml,
    /target: "#signin-button"\n\s+assert:\n\s+selector: "#signin-button"\n\s+visible: true/,
    `saw ${written.yaml}`,
  );
});

test("a dropped proposal on a step that NAVIGATED asserts the url it reached", async () => {
  // Where the step changed the document, saying which document it reached says what the
  // step DID — strictly more than "the button I clicked is gone". A navigation is a
  // page-level fact, and the grammar has a page-level assertion for it.
  const arrived = "http://app.test/orders/";
  const run = await runRecording(
    fakeDriver(),
    async () => true,
    emailEchoEvents(false, arrived),
    undefined,
    { assumeYes: true },
  );

  assert.equal(run.code, 0, `--yes must still write: ${run.errors.join("\n")}`);
  const warning = run.log.find((line) => /looks like email data/.test(line));
  assert.ok(
    warning,
    `the drop is still named — saw ${JSON.stringify(run.log)}`,
  );
  assert.match(
    warning,
    /fell back to asserting the url/,
    "and says WHICH fallback it used",
  );

  const written = run.written.at(-1);
  assert.ok(written, "a spec must be written");
  assert.ok(
    !/ops@example\.test/.test(written.yaml),
    `no address may reach the spec — saw ${written.yaml}`,
  );
  assert.match(
    written.yaml,
    new RegExp(
      `target: "#signin-button"\\n\\s+assert:\\n\\s+url: ${arrived.replace(/\//g, "\\/")}`,
    ),
    `the sign-in step asserts the url it reached — saw ${written.yaml}`,
  );
  // Exactly one url key, and the value canonical — the grammar refuses anything else,
  // and a spec that does not parse is not a spec.
  assert.ok(!/urlPrefix|urlPattern/.test(written.yaml), "exactly one url key");
});

test("--include-secrets keeps it — the human said so out loud", async () => {
  const run = await runRecording(
    fakeDriver(),
    async () => true,
    emailEchoEvents(),
    undefined,
    { assumeYes: true, includeSecrets: true },
  );
  assert.equal(run.code, 0, run.errors.join("\n"));
  assert.ok(
    run.written.some((one) => /ops@example\.test/.test(one.yaml)),
    "the opt-in is the whole of the difference",
  );
});

test("interactively the proposal is still OFFERED — the human is the control", async () => {
  const offered: string[] = [];
  await runRecording(
    fakeDriver(),
    async () => {
      return true;
    },
    emailEchoEvents(),
    undefined,
    {},
  );
  // Re-run capturing what the prompt was asked about.
  const seen: string[] = [];
  await runRecording(
    fakeDriver(),
    (async (proposal: { summary: string }) => {
      seen.push(proposal.summary);
      return false;
    }) as unknown as () => Promise<boolean>,
    emailEchoEvents(),
  );
  offered.push(...seen);
  assert.ok(
    offered.some((summary) => /ops@example\.test/.test(summary)),
    `an interactive run must still ask — saw ${JSON.stringify(offered)}`,
  );
});

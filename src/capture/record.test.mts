/**
 * Recording, against a real browser and a real page.
 *
 * The browser edge is the only thing that must be real here and it IS real: a Chromium
 * page, the bundle injected the way every gate injects rrweb, and input delivered as
 * trusted key and mouse events. What the recorder sees is what a human's hand produces;
 * only the hand is simulated.
 *
 * The properties under test are the ones a recording is worthless without: keystrokes
 * are ONE step, a consequence is folded but an ACTION never is, every control gets the
 * action that can replay it, a secret is not written down, and the human step log slices
 * the evidence stream exactly as a scripted one does — anchored on the moment the page
 * stamped the event, not the moment the host heard about it.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { Page } from "playwright-core";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import { serveDirectory } from "../replay/sample-app-server.mts";
import { isRenderable, sliceSegments } from "../evidence/segment.mts";
import type { ReplayEvent } from "../evidence/types.mts";
import { loadSpec, saveSpec } from "../spec/parse.mts";
import { replaySpec } from "../replay/runner.mts";
import type { DriverSession } from "../driver/types.mts";
import { locatorFor } from "../replay/assertions.mts";
import { createEventPump, recordFlow, type RecordedFlow } from "./record.mts";
import {
  attachCaptureListeners,
  framePathFacts,
  CAPTURE_BINDING,
  type CapturedEvent,
} from "./events.mts";
import { applyProposals } from "./proposals.mts";

const SAMPLE_APP = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
  "sample-app",
);

/** Everything a recording produced, plus the raw stream its step log must address. */
interface Recording {
  flow: RecordedFlow;
  events: ReplayEvent[] | null;
  baseUrl: string;
}

interface RecordOptions {
  includeSecrets?: boolean;
  after?: (recording: Recording) => Promise<void>;
  /** Collects the RAW bytes of every capture-binding payload, before the host has seen
   *  them. A secrecy claim asserted on the finished spec is a claim about a projection;
   *  this is the channel itself. */
  rawBinding?: string[];
}

/** Wrap the page's binding registration so every payload the page sends is recorded
 *  verbatim, in the order it crossed, and then handed on unchanged. */
function tapBinding(page: Page, into: string[]): void {
  const register = page.exposeBinding.bind(page);
  (page as unknown as { exposeBinding: Page["exposeBinding"] }).exposeBinding =
    ((
      name: string,
      callback: (source: unknown, ...args: unknown[]) => unknown,
    ) =>
      register(name, (source: unknown, ...args: unknown[]) => {
        if (typeof args[0] === "string") into.push(args[0]);
        return callback(source, ...args);
      })) as unknown as Page["exposeBinding"];
}

/** Hold every binding payload back by `ms` before handing it on — the process hop, made
 *  slow and deterministic, so the stop barrier is under test rather than the machine. */
function delayBindingDelivery(page: Page, ms: number): void {
  const register = page.exposeBinding.bind(page);
  (page as unknown as { exposeBinding: Page["exposeBinding"] }).exposeBinding =
    ((
      name: string,
      callback: (source: unknown, ...args: unknown[]) => unknown,
    ) =>
      register(name, (source: unknown, ...args: unknown[]) => {
        // Only the capture channel is slowed. The recording also asks the host whether it
        // is still running, and an answer deferred into a callback nobody awaits comes
        // back undefined — which would stop the bundle arming at all.
        if (name !== CAPTURE_BINDING) return callback(source, ...args);
        setTimeout(() => callback(source, ...args), ms);
        return undefined;
      })) as unknown as Page["exposeBinding"];
}

/** Write a one-off page set and serve it — for the shapes the sample app has no room
 *  for (a second document, a form with no button, a select and a checkbox). */
function pagesWith(files: Record<string, string>): string {
  const directory = mkdtempSync(join(tmpdir(), "formic-record-"));
  for (const [name, html] of Object.entries(files)) {
    writeFileSync(
      join(directory, name),
      `<!doctype html><meta charset="utf-8">${html}`,
    );
  }
  return directory;
}

/** Record one flow against `appDir`, with `drive` standing in for the human's hand. */
async function record(
  appDir: string,
  specName: string,
  drive: (page: Page, baseUrl: string) => Promise<void>,
  options: RecordOptions = {},
): Promise<Recording> {
  const app = await serveDirectory(appDir);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  let stopNow = () => {};
  const stop = new Promise<void>((resolve) => {
    stopNow = resolve;
  });
  if (options.rawBinding) tapBinding(session.page, options.rawBinding);
  try {
    const flowing = recordFlow({
      session,
      specName,
      driverName: driver.name,
      startUrl: `${app.baseUrl}/`,
      stop,
      includeSecrets: options.includeSecrets,
    });
    await session.page.waitForURL(`${app.baseUrl}/**`);
    await drive(session.page, app.baseUrl);
    // NO grace sleep. The binding hop is asynchronous and production has no sleep in it,
    // so a test that waits before stopping proves only that a sleep is long enough. The
    // recorder's own stop barrier is what must land the last event.
    stopNow();
    const flow = await flowing;
    const recording = {
      flow,
      events: await session.fetchReplay(),
    } as Recording;
    recording.baseUrl = app.baseUrl;
    if (options.after) await options.after(recording);
    return recording;
  } finally {
    await session.close();
    await app.close();
  }
}

/** The steps a human produced — the recorder's own opening `goto` is not one of them.
 *  A withheld value shows as its `valueFrom` reference: that IS the step's value half,
 *  and the whole claim these tests make is that the literal never appears anywhere. */
function humanSteps(
  flow: RecordedFlow,
): [string, string | undefined, string?][] {
  return flow.spec.steps
    .slice(1)
    .map(
      (step) =>
        [step.action, step.target, step.value ?? step.valueFrom] as [
          string,
          string | undefined,
          string?,
        ],
    );
}

test("typed text coalesces into ONE fill step carrying the final value", async () => {
  const { flow } = await record(
    SAMPLE_APP,
    "coalescing",
    async (page) => {
      await page.locator("#email").pressSequentially("ops@depot.test", {
        delay: 5,
      });
      await page
        .locator("#password")
        .pressSequentially("hunter2", { delay: 5 });
    },
    { includeSecrets: true },
  );
  assert.deepEqual(
    humanSteps(flow),
    [
      ["fill", "#email", "ops@depot.test"],
      ["fill", "#password", "hunter2"],
    ],
    "each field must be one fill step, not one step per keystroke",
  );
});

test("a navigation after a click is ONE click step, with the destination proposed", async () => {
  const pages = pagesWith({
    "index.html": '<h1>First page</h1><a id="go" href="next.html">Next</a>',
    "next.html": "<h1>Second page</h1>",
  });

  const { flow } = await record(pages, "navigating", async (page) => {
    await page.click("#go");
    await page.waitForURL("**/next.html");
  });

  assert.deepEqual(
    humanSteps(flow).map(([action, target]) => [action, target]),
    [["click", "#go"]],
    "the navigation the click caused must not become a step of its own",
  );
  assert.equal(flow.proposals.length, 1);
  assert.equal(flow.proposals[0].basis, "url");
  assert.deepEqual(flow.proposals[0].assertion, {
    role: "heading",
    name: "Second page",
    exact: true,
    hasText: "Second page",
  });
});

test("a navigation with NO action before it becomes its own goto step", async () => {
  // The defect this pins: every navigation was folded or dropped, so a page the human
  // reached by typing an address vanished from the spec — a replay of which skipped a
  // whole document and then clicked on the wrong one.
  const pages = pagesWith({
    "index.html": "<h1>First page</h1>",
    "second.html": '<h1>Second page</h1><button id="act">Act</button>',
  });

  const { flow } = await record(pages, "standalone-nav", async (page, base) => {
    await page.goto(`${base}/second.html`);
    await page.click("#act");
  });

  assert.deepEqual(
    humanSteps(flow).map(([action]) => action),
    ["goto", "click"],
    "the page the human navigated to must be in the spec",
  );
  assert.match(flow.spec.steps[1].target ?? "", /second\.html$/);
  assert.equal(
    flow.proposals.length,
    1,
    "a goto asserts by its own nature; only the click proposes",
  );
});

test("an unreferred navigation is the HUMAN's, however soon it follows a click", async () => {
  // Time was the whole test: any navigation within two seconds of any click was folded
  // into it. Click an inert control and then go somewhere by hand and the goto
  // disappeared — a replay of that spec skipped a whole document. The browser's own
  // causal signal decides now: a navigation the previous document initiated carries a
  // referrer, one the human typed carries none, and no clock is consulted either way.
  const pages = pagesWith({
    "index.html": '<h1>First page</h1><button id="act">Act</button>',
    "second.html": '<h1>Second page</h1><button id="done">Done</button>',
  });

  const { flow } = await record(pages, "unreferred-nav", async (page, base) => {
    await page.click("#act");
    await page.goto(`${base}/second.html`);
    await page.click("#done");
  });

  assert.deepEqual(
    humanSteps(flow).map(([action, target]) => [action, target]),
    [
      ["click", "#act"],
      ["goto", `${flow.spec.startUrl.replace(/\/$/, "")}/second.html`],
      ["click", "#done"],
    ],
    "a navigation the click did not cause is the human's, no matter how soon it arrives",
  );
});

test("a click's navigation is ITS consequence however long it takes to arrive", async () => {
  // The mirror image, and the one the clock got wrong in the other direction: a slow
  // load or a redirect chain announced after the settle window, so the real consequence
  // of the click became a redundant goto step in the middle of the spec.
  const pages = pagesWith({
    "index.html": `<h1>First page</h1>
      <button id="act" onclick="setTimeout(function(){location.href='second.html'},3000)">Act</button>`,
    "second.html": '<h1>Second page</h1><button id="done">Done</button>',
  });

  const { flow } = await record(pages, "slow-nav", async (page) => {
    await page.click("#act");
    await page.waitForURL("**/second.html", { timeout: 15000 });
    await page.click("#done");
  });

  assert.deepEqual(
    humanSteps(flow).map(([action, target]) => [action, target]),
    [
      ["click", "#act"],
      ["click", "#done"],
    ],
    "three seconds is still the click's own consequence — causation is not elapsed time",
  );
});

test("a delayed navigation belongs to the action that CAUSED it, never the one in flight", async () => {
  // The referrer says a navigation was page-initiated; it does not say WHICH action
  // started it. Folding every referred load into whichever action happens to be open
  // hands A's slow consequence to B — the wrong step grows a page it never reached, and
  // B's own proposal describes a document B did not produce. The activation carries the
  // action's id across the load instead.
  const pages = pagesWith({
    "index.html": `<h1>First</h1>
      <button id="a" onclick="setTimeout(function(){location.href='second.html'},2500)">A</button>
      <button id="b">B</button>`,
    "second.html": '<h1>Second</h1><button id="done">Done</button>',
  });

  const { flow } = await record(pages, "cause-not-flight", async (page) => {
    await page.click("#a");
    await page.click("#b");
    await page.waitForURL("**/second.html", { timeout: 15000 });
    await page.click("#done");
  });

  const steps = humanSteps(flow).map(([action, target]) => [action, target]);
  const bIndex = steps.findIndex(([, target]) => target === "#b");
  assert.ok(bIndex >= 0, `B must be a step — saw ${JSON.stringify(steps)}`);
  assert.notDeepEqual(
    steps[bIndex + 1],
    ["click", "#done"],
    "A's navigation must not vanish into B — B never caused it",
  );
});

test("a scripted navigation nobody asked for is a goto, not somebody's consequence", async () => {
  // No activation is open at all: the page navigates itself, the way a splash screen or
  // an expired session does. Nobody's consequence, so nobody's step.
  const pages = pagesWith({
    "index.html": `<h1>First</h1>
      <script>setTimeout(function(){location.href='second.html'},900)</script>`,
    "second.html": '<h1>Second</h1><button id="done">Done</button>',
  });

  const { flow } = await record(pages, "unasked-nav", async (page) => {
    await page.waitForURL("**/second.html", { timeout: 15000 });
    await page.click("#done");
  });

  assert.deepEqual(
    humanSteps(flow).map(([action]) => action),
    ["goto", "click"],
    "no user activation caused it, so it is the human's own move",
  );
});

test("a click navigation folds even with NO referrer — the id crossed, not the header", async () => {
  // Referrer-Policy: no-referrer strips the browser's corroborating signal. The
  // activation id is written where the next document can read it, so ownership survives
  // a policy that was never about causation in the first place.
  const pages = pagesWith({
    "index.html": `<h1>First</h1><meta name="referrer" content="no-referrer">
      <a id="go" href="next.html">Next</a>`,
    "next.html": '<h1>Second</h1><button id="done">Done</button>',
  });

  const { flow } = await record(pages, "no-referrer", async (page) => {
    await page.click("#go");
    await page.waitForURL("**/next.html");
    await page.click("#done");
  });

  assert.deepEqual(
    humanSteps(flow).map(([action, target]) => [action, target]),
    [
      ["click", "#go"],
      ["click", "#done"],
    ],
    "the navigation the click caused must not become a goto for want of a header",
  );
});

test("a delayed pushState after an unrelated input is a goto, not that input's route", async () => {
  // Typing is not an activation. A route that arrives long after the keystrokes that
  // preceded it was caused by something the recorder cannot see, and the honest answer
  // is a goto rather than blaming whichever field was last touched.
  const pages = pagesWith({
    "index.html": `<h1>App</h1>
      <input id="one"><input id="two">
      <button id="done">Done</button>
      <script>
        document.querySelector('#one').addEventListener('input', function () {
          setTimeout(function () { history.pushState({}, '', '/late'); }, 1200);
        }, { once: true });
      </script>`,
  });

  const { flow } = await record(pages, "late-route", async (page) => {
    await page.locator("#one").pressSequentially("ab", { delay: 5 });
    await page.locator("#two").pressSequentially("cd", { delay: 5 });
    // Deliberately NOT blurred: a blur fires a change, and a change IS an activation a
    // route may belong to. Blurring here would hand the route to the field the human
    // happened to leave, which is the very attribution this test exists to refuse.
    await page.waitForTimeout(1600);
    await page.click("#done");
  });

  // The trailing fill is #two's own change, which fires when the click on #done blurs
  // it — on the far side of the route, so it opens a step of its own. That is the
  // coalescer being right about a boundary, not a second interaction.
  assert.deepEqual(
    humanSteps(flow).map(([action]) => action),
    ["fill", "fill", "goto", "fill", "click"],
    `a route no activation owns is a goto — saw ${JSON.stringify(humanSteps(flow))}`,
  );
});

test("a same-document navigation announces itself — hash, pushState and popstate alike", async () => {
  // Only a document DOMContentLoaded emitted a navigation, so a single-page application
  // moved between screens and the recording said nothing happened at all.
  const pages = pagesWith({
    "index.html": '<h1>App</h1><button id="done">Done</button>',
  });

  const { flow } = await record(pages, "same-document", async (page) => {
    await page.evaluate(() => {
      history.pushState({}, "", "/orders");
    });
    await page.evaluate(() => {
      location.hash = "#detail";
    });
    await page.evaluate(() => {
      history.back();
    });
    await page.waitForTimeout(150);
    await page.click("#done");
  });

  const actions = humanSteps(flow).map(([action]) => action);
  assert.deepEqual(
    actions,
    ["goto", "goto", "goto", "click"],
    `each same-document navigation must be a step of its own — saw ${JSON.stringify(humanSteps(flow))}`,
  );
});

test("an interaction inside an IFRAME becomes a step ADDRESSED to that frame, and replays", async () => {
  // `exposeBinding` reaches EVERY frame — measured — so a child document reports its
  // clicks through the same channel the page does. They used to be refused outright:
  // the grammar had no frame, so a frame-local selector as a top-level step addressed
  // the wrong document or nothing at all. The grammar carries a chain now, so the host
  // NAMES the frame instead of dropping what the human did.
  //
  // Driven through the PRODUCTION installation path, with nothing injected by hand, and
  // the recorded spec is then REPLAYED — a chain that only looks right is worth nothing.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe id="child" name="child-frame" src="child.html"></iframe>
      <button id="done">Done</button>`,
    "child.html": `<h1>Child</h1><input id="code" /><button id="inner">Inner</button>
      <p data-testid="echo">idle</p>
      <script>
        document.getElementById("inner").addEventListener("click", function () {
          document.querySelector('[data-testid="echo"]').textContent =
            "got " + document.getElementById("code").value;
        });
      </script>`,
  });

  const { flow, baseUrl } = await record(pages, "iframe", async (page) => {
    await page.frameLocator("#child").locator("#code").fill("77");
    await page.frameLocator("#child").locator("#inner").click();
    await page.click("#done");
  });

  assert.deepEqual(
    humanSteps(flow).map(([action, target]) => [action, target]),
    [
      ["fill", "#code"],
      ["click", "#inner"],
      ["click", "#done"],
    ],
    `every step must be recorded — saw ${JSON.stringify(humanSteps(flow))} ${JSON.stringify(flow.warnings)}`,
  );
  const [fill, inner, done] = flow.spec.steps.slice(1);
  assert.deepEqual(fill.frame, [{ selector: "#child" }]);
  assert.deepEqual(inner.frame, [{ selector: "#child" }]);
  assert.equal(done.frame, undefined, "a top-level click carries no chain");
  assert.deepEqual(flow.warnings, []);

  // And it replays. The proof the chain is real: the same spec, driven back through the
  // runner against the same pages.
  const confirmed = applyProposals(flow.spec, flow.proposals);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  const app = await serveDirectory(pages);
  try {
    const replayed = await replaySpec(
      {
        ...confirmed,
        steps: confirmed.steps.map((step) =>
          step.action === "goto"
            ? { ...step, target: `${app.baseUrl}/index.html` }
            : step,
        ),
      },
      session,
      { stepTimeoutMs: 2000 },
    );
    assert.equal(
      replayed.outcome,
      "passed",
      `${JSON.stringify(replayed.failure)} — recorded against ${baseUrl}`,
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("keystrokes in two frames do not coalesce into one step, however alike the fields", async () => {
  // THE document-id guard. Two documents live at once, the same `#code` in both, and no
  // navigation anywhere — so the only thing that can keep the two entries apart is
  // coalescing's key, and removing that key turns this red. Coalescing on the selector
  // alone folded the second frame's typing into the first frame's step, and the spec then
  // typed both values into one field.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1>
      <iframe id="left" src="child.html"></iframe>
      <iframe id="right" src="child.html"></iframe>`,
    "child.html": '<input id="code" />',
  });

  const { flow } = await record(pages, "two-frames", async (page) => {
    await page.frameLocator("#left").locator("#code").fill("aa");
    await page.frameLocator("#right").locator("#code").fill("bb");
  });

  const fills = flow.spec.steps.filter((step) => step.action === "fill");
  assert.equal(fills.length, 2, JSON.stringify(humanSteps(flow)));
  assert.deepEqual(fills[0].frame, [{ selector: "#left" }]);
  assert.deepEqual(fills[1].frame, [{ selector: "#right" }]);
  assert.equal(fills[0].value, "aa");
  assert.equal(fills[1].value, "bb");
});

test("a frame nobody can address is still refused BY NAME, never folded into the page", async () => {
  // The refusal did not go away, it narrowed. A `srcdoc` frame with no id, no test id
  // and no name has no owning-element selector to prove, no name to fall back to, and no
  // http(s) document URL — so nothing in the grammar can address it, and a recording
  // that silently dropped the click would leave the human reading a spec missing what
  // they did with no way to know it.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1>
      <iframe srcdoc="<button id='inner'>Inner</button>"></iframe>
      <button id="done">Done</button>`,
  });

  const { flow } = await record(pages, "unaddressable", async (page) => {
    await page.frameLocator("iframe").locator("#inner").click();
    await page.click("#done");
  });

  assert.deepEqual(
    humanSteps(flow).map(([action, target]) => [action, target]),
    [["click", "#done"]],
    `an unaddressable frame's click must not become a top-level step — saw ${JSON.stringify(humanSteps(flow))}`,
  );
  assert.ok(
    flow.warnings.some(
      (line) =>
        /nested frame/.test(line) && /nothing in the spec grammar/.test(line),
    ),
    `the refusal must say why it refused — saw ${JSON.stringify(flow.warnings)}`,
  );
});

test("every emitted locator RESOLVES, and a target with none refuses the step by name", async () => {
  // The fallback used to be a positional CSS path — the address of a POSITION, invalid
  // at body, impossible at html, and unrelated to its host under a shadow root. It made
  // every target look derivable while some of them resolved nothing. Nothing is emitted
  // now that the page has not proven resolves to exactly one element and to the element
  // the event was about; a target with no such candidate refuses its step and says so.
  const pages = pagesWith({
    "index.html": `<h1>Controls</h1>
      <input id="named" type="search">
      <div><input type="search" placeholder="Anonymous"></div>
      <select multiple id="picker">
        <option value="a">A</option><option value="b">B</option>
      </select>
      <button id="twin">First twin</button>
      <button id="twin">Second twin</button>
      <h2>Repeated section</h2>
      <h2>Repeated section</h2>
      <h3 aria-label="Not what it says">Visible words</h3>
      <span data-testid="tag">a label</span>
      <button data-testid="tag">Shared tag</button>
      <button id="done">Done</button>`,
  });

  const recorded = await record(pages, "resolvable", async (page) => {
    await page.locator("#named").pressSequentially("widgets", { delay: 3 });
    await page.locator('input[placeholder="Anonymous"]').click();
    await page.selectOption("#picker", "b");
    // An id and a test id are only addresses when they are UNIQUE. The page must prove
    // that before reporting either, and prove the one element it resolves is this one —
    // an id repeated twice resolves the FIRST, so emitting it records a click on the
    // wrong control that replays green forever.
    await page.locator("text=Second twin").click();
    await page.locator("button[data-testid=tag]").click();
    await page.click("#done");
  });

  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/`);
    for (const step of recorded.flow.spec.steps) {
      if (step.action === "goto" || step.target === undefined) continue;
      const resolved = session.page.locator(step.target);
      assert.equal(
        await resolved.count(),
        1,
        `step ${step.index} emitted ${JSON.stringify(step.target)}, which resolves ${await resolved.count()} elements`,
      );
    }
  } finally {
    await session.close();
    await app.close();
  }

  // And the ASSERTIONS too. A step target was proven while the assertion beside it was
  // synthesised from DOM text and never resolved against anything — duplicate headings
  // and an aria-label that disagrees with the visible words both emit a role/name pair
  // that finds nothing, or finds two things.
  const withAssertions = applyProposals(
    recorded.flow.spec,
    recorded.flow.proposals,
  );
  const appAgain = await serveDirectory(pages);
  const proofDriver = new LocalPlaywrightDriver();
  const proofSession = await proofDriver.open();
  try {
    await proofSession.page.goto(`${appAgain.baseUrl}/`);
    // A loop over an empty list passes without checking anything, so the list is
    // checked first: a flow that asserted NOTHING is a finding, not a green test.
    const asserted = withAssertions.steps.filter(
      (one) => one.assert !== undefined,
    );
    assert.ok(
      asserted.length > 0,
      "the flow must assert something, or this proves nothing",
    );
    for (const step of asserted) {
      const resolved = locatorFor(proofSession.page, step.assert!);
      assert.equal(
        await resolved.count(),
        1,
        `step ${step.index} asserts ${JSON.stringify(step.assert)}, which resolves ${await resolved.count()} elements`,
      );
    }
  } finally {
    await proofSession.close();
    await appAgain.close();
  }

  const anonymous = recorded.flow.spec.steps.filter(
    (step) => step.target !== undefined && /Anonymous/.test(step.target),
  );
  assert.deepEqual(anonymous, [], "an unprovable target is never written down");
  assert.ok(
    recorded.flow.warnings.some((line) => /could not be addressed/.test(line)),
    `the refusal must be said out loud — saw ${JSON.stringify(recorded.flow.warnings)}`,
  );
});

test("an assertion locator is PROVEN too, or it is not proposed", async () => {
  // Step targets were proven; the assertion beside them was synthesised from DOM text
  // and resolved against nothing. Two headings that read the same emit a role/name pair
  // matching both, and a spec whose assertion resolves two elements fails on the very
  // page it was recorded from — or worse, passes on the wrong one.
  const pages = pagesWith({
    "index.html": '<h1>First page</h1><a id="go" href="next.html">Next</a>',
    // The addressable node comes FIRST: `firstAppeared` takes the first appeared node
    // and abandons the proposal outright if that one cannot be addressed, so leading
    // with the duplicate headings made this test assert nothing at all — which is what
    // the non-empty guard below now catches. The duplicates still follow, and are still
    // what must not be emitted.
    "next.html": `<p data-testid="only-one">Unambiguous</p>
      <h1>Repeated heading</h1><h1>Repeated heading</h1>
      <button id="after">After</button>`,
  });

  const recorded = await record(pages, "proven-assertions", async (page) => {
    await page.click("#go");
    await page.waitForURL("**/next.html");
    // A step's window closes when the NEXT event opens one, so the navigating click
    // needs a successor before it can propose anything.
    await page.click("#after");
  });

  const spec = applyProposals(recorded.flow.spec, recorded.flow.proposals);
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/next.html`);
    // A loop over an empty list passes without checking anything, so the list is
    // checked first: a flow that asserted NOTHING is a finding, not a green test.
    const asserted = spec.steps.filter((one) => one.assert !== undefined);
    assert.ok(
      asserted.length > 0,
      "the flow must assert something, or this proves nothing",
    );
    for (const step of asserted) {
      const resolved = locatorFor(session.page, step.assert!);
      assert.equal(
        await resolved.count(),
        1,
        `step ${step.index} asserts ${JSON.stringify(step.assert)}, which resolves ${await resolved.count()} elements`,
      );
    }
  } finally {
    await session.close();
    await app.close();
  }
});

test("a long heading is asserted WHOLE — an exact match on a prefix is a broken spec", async () => {
  // Display text is capped for readability. An exact NAME is a correctness question and
  // has no business sharing that budget: sliced to 120 characters and then matched
  // whole-string, a long heading could never satisfy the assertion recorded from it.
  const heading = `Order ${"A".repeat(160)} approved`;
  const pages = pagesWith({
    "index.html": '<h1>First page</h1><a id="go" href="next.html">Next</a>',
    "next.html": `<h1>${heading}</h1>`,
  });

  const recorded = await record(pages, "long-heading", async (page) => {
    await page.click("#go");
    await page.waitForURL("**/next.html");
  });

  const spec = applyProposals(recorded.flow.spec, recorded.flow.proposals);
  const asserted = spec.steps.find((step) => step.assert?.role === "heading");
  assert.ok(
    asserted,
    `the heading must be proposed — saw ${JSON.stringify(spec.steps.map((step) => step.assert))}`,
  );
  assert.equal(
    asserted.assert?.name,
    heading,
    "the exact name must be the whole heading, not a display-budget slice",
  );

  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/next.html`);
    assert.equal(
      await locatorFor(session.page, asserted.assert!).count(),
      1,
      "and it must resolve",
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("a heading name is proven against the ACCESSIBLE name, not the DOM text", async () => {
  // The uniqueness check compared textContent, and an accessible name is not textContent.
  // `<h2 aria-label="Ready">Another</h2>` reads as "Another" in the DOM and as "Ready" to
  // an engine, so a page with an `<h1>Ready</h1>` looked unambiguous and emitted an exact
  // heading locator that resolves TWO elements. Measured in Chromium by the reviewer.
  const pages = pagesWith({
    "index.html": '<h1>First page</h1><a id="go" href="next.html">Next</a>',
    "next.html": `<h1>Ready</h1><h2 aria-label="Ready">Another</h2>
      <p data-testid="only-one">Unambiguous</p>`,
  });

  const recorded = await record(pages, "aria-name-collision", async (page) => {
    await page.click("#go");
    await page.waitForURL("**/next.html");
  });

  const spec = applyProposals(recorded.flow.spec, recorded.flow.proposals);
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/next.html`);
    // A loop over an empty list passes without checking anything, so the list is
    // checked first: a flow that asserted NOTHING is a finding, not a green test.
    const asserted = spec.steps.filter((one) => one.assert !== undefined);
    assert.ok(
      asserted.length > 0,
      "the flow must assert something, or this proves nothing",
    );
    for (const step of asserted) {
      const resolved = locatorFor(session.page, step.assert!);
      assert.equal(
        await resolved.count(),
        1,
        `step ${step.index} asserts ${JSON.stringify(step.assert)}, which resolves ${await resolved.count()} elements`,
      );
    }
  } finally {
    await session.close();
    await app.close();
  }
});

test("a name too long to be sensible is REFUSED by name, never silently cut", async () => {
  // The exact name was capped at 2,000 characters. A longer heading produced a name that
  // is a PREFIX of the real one and matched nothing at all — a silent cut that turns into
  // a spec failing on the page it came from. Measured in Chromium: 2,100 characters in,
  // a 2,000-character exact name out, resolving zero.
  const heading = `Order ${"A".repeat(2400)} approved`;
  const pages = pagesWith({
    "index.html": '<h1>First page</h1><a id="go" href="next.html">Next</a>',
    "next.html": `<h1>${heading}</h1><p data-testid="fallback">Landed</p>`,
  });

  const recorded = await record(pages, "unsensible-name", async (page) => {
    await page.click("#go");
    await page.waitForURL("**/next.html");
  });

  const spec = applyProposals(recorded.flow.spec, recorded.flow.proposals);
  const namesAsserted = spec.steps
    .map((step) => step.assert?.name)
    .filter((name): name is string => name !== undefined);
  for (const name of namesAsserted) {
    assert.ok(
      heading.startsWith(name) === false || name === heading,
      `a name must be whole or absent, never a prefix — saw ${name.length} of ${heading.length} characters`,
    );
  }
  assert.ok(
    recorded.flow.warnings.some((warning) => /too long/.test(warning)),
    `the refusal must be named — saw ${JSON.stringify(recorded.flow.warnings)}`,
  );

  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/next.html`);
    // A loop over an empty list passes without checking anything, so the list is
    // checked first: a flow that asserted NOTHING is a finding, not a green test.
    const asserted = spec.steps.filter((one) => one.assert !== undefined);
    assert.ok(
      asserted.length > 0,
      "the flow must assert something, or this proves nothing",
    );
    for (const step of asserted) {
      assert.equal(
        await locatorFor(session.page, step.assert!).count(),
        1,
        `step ${step.index} asserts ${JSON.stringify(step.assert)}`,
      );
    }
  } finally {
    await session.close();
    await app.close();
  }
});

test("a colliding aria-label whose name contains an s is still a collision", async () => {
  // The regex bug hid behind name choice: with /s+/g, "Orders" normalises to "Order" and
  // the collision vanishes. The earlier test used "Ready", which happens to contain no
  // lowercase s, so it passed over the defect.
  const pages = pagesWith({
    "index.html": '<h1>First page</h1><a id="go" href="next.html">Next</a>',
    "next.html": `<h1>Orders</h1><h2 aria-label="Orders">Another</h2>
      <p data-testid="only-one">Unambiguous</p>`,
  });

  const recorded = await record(pages, "s-collision", async (page) => {
    await page.click("#go");
    await page.waitForURL("**/next.html");
  });

  const spec = applyProposals(recorded.flow.spec, recorded.flow.proposals);
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/next.html`);
    const asserted = spec.steps.filter((step) => step.assert !== undefined);
    assert.ok(
      asserted.length > 0,
      "the flow must assert something, or this proves nothing",
    );
    for (const step of asserted) {
      assert.equal(
        await locatorFor(session.page, step.assert!).count(),
        1,
        `step ${step.index} asserts ${JSON.stringify(step.assert)}`,
      );
    }
  } finally {
    await session.close();
    await app.close();
  }
});

test("a name is normalised BEFORE the traversal bound, not after", async () => {
  // The bound counted raw characters, so a run of whitespace could exhaust it before the
  // traversal reached the rest of the heading — and the name came back "A", a prefix of
  // the real one, silently. Whitespace collapses as it is collected, so the budget
  // measures the name as it will actually be asserted.
  const heading = `A${" ".repeat(8100)}<span>B</span>`;
  const pages = pagesWith({
    "index.html": '<h1>First page</h1><a id="go" href="next.html">Next</a>',
    "next.html": `<h1>${heading}</h1><p data-testid="landed">Landed</p>`,
  });

  const recorded = await record(pages, "normalise-first", async (page) => {
    await page.click("#go");
    await page.waitForURL("**/next.html");
  });

  const spec = applyProposals(recorded.flow.spec, recorded.flow.proposals);
  const named = spec.steps.find((step) => step.assert?.role === "heading");
  assert.ok(
    named,
    `the heading is well within the cap once normalised, so it must be claimed — saw ${JSON.stringify(spec.steps.map((step) => step.assert))}`,
  );
  assert.equal(
    named.assert?.name,
    "A B",
    "the whole name, whitespace collapsed — not a prefix",
  );

  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/next.html`);
    assert.equal(
      await locatorFor(session.page, named.assert!).count(),
      1,
      "and it must resolve",
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("a form submitted from the KEYBOARD becomes a press Enter step", async () => {
  // A form with no submit button has no click to fold the submission into. Dropping the
  // submit lost the action entirely: the spec typed into a field and stopped.
  const pages = pagesWith({
    "index.html": `<h1>Search</h1>
      <form id="f" onsubmit="event.preventDefault();document.querySelector('h1').textContent='Results'">
        <input id="q" name="q">
      </form>`,
  });

  const { flow } = await record(pages, "keyboard-submit", async (page) => {
    await page.locator("#q").pressSequentially("widgets", { delay: 5 });
    await page.locator("#q").press("Enter");
  });

  assert.deepEqual(
    humanSteps(flow),
    [
      ["fill", "#q", "widgets"],
      ["press", "#q", "Enter"],
    ],
    "the keyboard submission is an action, and no other event describes it",
  );
});

test("ONE human gesture is ONE step, and the spec it writes replays", async () => {
  // The old body drove `selectOption` and `check` — API calls that fire untrusted input
  // and change events and NO click at all — then compared the resulting tuple and
  // stopped. It could not see either defect it was named for.
  //
  // A label click dispatches TWO trusted clicks in Chromium, the label's and the one it
  // forwards to the control, so the recording toggled the checkbox twice on replay. A
  // select's click was emitted unconditionally while its change separately became a
  // select, so one choice was two steps. Both gestures are driven here as a pointer
  // makes them, and the spec is REPLAYED: the end state is the proof.
  //
  // The select's value is set through the API after its real click because a native
  // <select> cannot be chosen from by synthetic input in Chromium AT ALL — measured both
  // headless and headed: focus plus ArrowDown plus Enter fires no click, no input and no
  // change, and does not move the value. The popup is browser chrome the page never
  // sees. So the half that CAN be real is real — the trusted pointer click on the
  // control — and it is asserted at the channel below, not inferred from the step list.
  const pages = pagesWith({
    "index.html": `<h1>Controls</h1>
      <label id="agree-label" for="agree">Agree to terms</label>
      <input id="agree" type="checkbox">
      <select id="picker"><option value="a">A</option><option value="b">B</option></select>
      <textarea id="note"></textarea>
      <div id="rich" contenteditable="true"></div>`,
  });

  const rawBinding: string[] = [];
  const { flow } = await record(
    pages,
    "gestures",
    async (page) => {
      await page.click("#agree-label");
      await page.locator("#picker").click();
      await page.selectOption("#picker", "b");
      await page.locator("#note").pressSequentially("note", { delay: 5 });
      await page.locator("#rich").pressSequentially("rich", { delay: 5 });
    },
    { rawBinding },
  );

  // At the CHANNEL: the trusted pointer click on the select, and the browser's forwarded
  // click on the checkbox, must never have crossed at all. A step list can be right for
  // the wrong reason; the payloads cannot.
  const crossed = rawBinding.map(
    (json) => JSON.parse(json) as { kind: string; tagName?: string },
  );
  assert.equal(
    crossed.filter(
      (event) => event.kind === "click" && event.tagName === "select",
    ).length,
    0,
    "a select's click is its popup opening, and must not cross",
  );
  assert.equal(
    crossed.filter(
      (event) => event.kind === "click" && event.tagName === "input",
    ).length,
    0,
    "the click the browser forwards from a label must not cross either",
  );

  assert.deepEqual(humanSteps(flow), [
    ["click", "#agree-label", undefined],
    ["select", "#picker", "b"],
    ["fill", "#note", "note"],
    ["fill", "#rich", "rich"],
  ]);

  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const spec = applyProposals(flow.spec, flow.proposals);
    const rebased = {
      ...spec,
      startUrl: `${app.baseUrl}/`,
      steps: spec.steps.map((step) =>
        step.action === "goto" ? { ...step, target: `${app.baseUrl}/` } : step,
      ),
    };
    const result = await replaySpec(rebased, session);
    assert.equal(
      result.outcome,
      "passed",
      `replay failed: ${result.failure?.error ?? ""}`,
    );
    assert.equal(
      await session.page.isChecked("#agree"),
      true,
      "one click on the label must leave the box CHECKED — twice is off again",
    );
    assert.equal(await session.page.inputValue("#picker"), "b");
  } finally {
    await session.close();
    await app.close();
  }
});

test("personal data is recorded as an environment reference, never as the value", async () => {
  const { flow } = await record(SAMPLE_APP, "secrets", async (page) => {
    await page.locator("#email").pressSequentially("ops@depot.test", {
      delay: 5,
    });
    await page.locator("#password").pressSequentially("hunter2", { delay: 5 });
  });

  // The name is DERIVED, not invented per run: the spec name, the field, the category,
  // with the part that repeats another dropped. Recording this flow twice has to ask for
  // the same variable, or the value the human already supplied is silently orphaned.
  assert.deepEqual(humanSteps(flow), [
    ["fill", "#email", "env.SECRETS_EMAIL"],
    ["fill", "#password", "env.SECRETS_PASSWORD"],
  ]);
  assert.deepEqual(
    flow.secrets.map((secret) => [
      secret.target,
      secret.category,
      secret.variable,
    ]),
    [
      ["#email", "email", "SECRETS_EMAIL"],
      ["#password", "password", "SECRETS_PASSWORD"],
    ],
    "each withheld step must be nameable, categorised AND resolvable",
  );
  const written = saveSpec(applyProposals(flow.spec, flow.proposals));
  assert.doesNotMatch(written, /hunter2/, "no secret may reach the spec file");
  assert.doesNotMatch(
    written,
    /ops@depot\.test/,
    "a typed email address is personal data, not a fixture",
  );
  assert.match(written, /valueFrom: env\.SECRETS_EMAIL/);
  assert.match(written, /valueFrom: env\.SECRETS_PASSWORD/);
  // And what it writes is a spec the product's own loader accepts — a reference that
  // does not load is no better than the placeholder it replaced.
  assert.equal(loadSpec(written).steps[1].valueFrom, "env.SECRETS_EMAIL");
});

test("personal data never crosses EITHER channel out of the page", async () => {
  // This test used to inspect the finished spec — a claim about one projection, made
  // downstream of the leak it was named for. Both raw channels are asserted here
  // instead: the capture binding's own bytes, before the host has transformed anything,
  // and the replay stream, which is a second binding the capture redaction never
  // touched. A contenteditable is included because its typed text never went through
  // the value channel at all — it rode out as the target's `text` fact and as a node in
  // the visible state, where nothing was withholding anything.
  //
  // The editable is NESTED inside a labelled heading on purpose. Masking only the node
  // and its ancestors left the CONTAINER reporting its aggregate textContent, so the
  // same secret walked out through the panel around it — a sibling of the leak, not the
  // leak itself.
  const pages = pagesWith({
    "index.html": `<h1>Checkout</h1>
      <label for="email">Email address</label><input id="email" type="text">
      <input id="card" type="text" autocomplete="cc-number">
      <input id="home" type="text" autocomplete="street-address">
      <h2 id="ident-heading" data-testid="ident-panel">Applicant
        <div id="ident" contenteditable="true" aria-label="Passport number"></div>
      </h2>
      <label for="note">Approval note</label><input id="note" type="text">`,
  });
  const rawBinding: string[] = [];
  const sentinels = {
    email: "zqemail@depot.test",
    card: "4111111111111111",
    home: "17 Zqharbour Road",
    ident: "ZQ1234567",
  };
  const { events } = await record(
    pages,
    "raw-channels",
    async (page) => {
      for (const [id, typed] of Object.entries(sentinels)) {
        await page.locator(`#${id}`).click();
        await page.locator(`#${id}`).pressSequentially(typed, { delay: 3 });
      }
      await page.locator("#note").pressSequentially("ship next week", {
        delay: 3,
      });
    },
    { rawBinding },
  );

  assert.ok(rawBinding.length > 0, "the binding tap must have seen traffic");
  const channels: [string, string][] = [
    ["the capture binding", rawBinding.join("\n")],
    ["the replay stream", JSON.stringify(events ?? [])],
  ];
  for (const [channel, payload] of channels) {
    for (const [field, typed] of Object.entries(sentinels)) {
      assert.ok(
        !payload.includes(typed),
        `${field}'s value reached ${channel} — a secret that crossed is already in another process's memory`,
      );
    }
    assert.ok(
      payload.includes("ship next week"),
      `an ordinary field must still reach ${channel}`,
    );
  }
});

test("the canonical classification is the ONLY sensitivity on the wire", async () => {
  // Classification was memoised once page-side and then COPIED into three parallel
  // fields — event.sensitive, TargetFacts.sensitive, VisibleNode.sensitive — each read
  // by a different consumer. The single record was dead metadata nobody consulted, which
  // is the policy-boundary class re-instantiated inside the fix for it. There is one
  // field, and every projection reads it.
  const rawBinding: string[] = [];
  const { flow } = await record(
    SAMPLE_APP,
    "one-record",
    async (page) => {
      await page.locator("#email").pressSequentially("ops@depot.test", {
        delay: 3,
      });
    },
    { rawBinding },
  );
  const payloads = rawBinding.map(
    (json) => JSON.parse(json) as Record<string, unknown>,
  );
  assert.ok(payloads.length > 0);
  for (const payload of payloads) {
    assert.equal(
      typeof payload.sensitive,
      "undefined",
      `a copied sensitivity field is still on the wire: ${JSON.stringify(payload).slice(0, 200)}`,
    );
    const target = payload.target as Record<string, unknown> | undefined;
    if (target) assert.equal(typeof target.sensitive, "undefined");
    const state = payload.state as { nodes?: Record<string, unknown>[] };
    for (const node of state?.nodes ?? []) {
      assert.equal(typeof node.sensitive, "undefined");
    }
  }
  const withheld = payloads.find(
    (payload) => payload.classification !== undefined,
  );
  assert.ok(withheld, "the canonical record must be the one that IS carried");
  // And it is the record the host acted on, end to end.
  assert.deepEqual(humanSteps(flow), [
    ["fill", "#email", "env.ONE_RECORD_EMAIL"],
  ]);
  assert.deepEqual(
    flow.secrets.map((secret) => secret.category),
    ["email"],
  );
});

test("every classification route redacts, and an ordinary field is left alone", async () => {
  // One field per route, in a real browser, so the injected matcher is the code under
  // test — not a host-side stand-in that could agree with a page-side copy that drifted.
  const pages = pagesWith({
    "index.html": `<h1>Checkout</h1>
      <input id="phone" type="tel">
      <input id="card" type="text" autocomplete="cc-number">
      <input id="who" type="text" name="passportNumber">
      <textarea id="note" placeholder="Optional note for the audit trail"></textarea>`,
  });

  const { flow } = await record(pages, "categories", async (page) => {
    await page.locator("#phone").pressSequentially("07700900123", { delay: 3 });
    await page.locator("#card").pressSequentially("4111111111111111", {
      delay: 3,
    });
    await page.locator("#who").pressSequentially("X1234567", { delay: 3 });
    await page.locator("#note").pressSequentially("ship next week", {
      delay: 3,
    });
  });

  assert.deepEqual(humanSteps(flow), [
    ["fill", "#phone", "env.CATEGORIES_PHONE"],
    ["fill", "#card", "env.CATEGORIES_CARD_PAYMENT"],
    ["fill", "#who", "env.CATEGORIES_WHO_IDENTIFICATION"],
    ["fill", "#note", "ship next week"],
  ]);
  const written = saveSpec(applyProposals(flow.spec, flow.proposals));
  for (const value of ["07700900123", "4111111111111111", "X1234567"]) {
    assert.doesNotMatch(
      written,
      new RegExp(value),
      `${value} must not reach the spec file`,
    );
  }
});

test("--include-secrets is the human saying so, and records the real value", async () => {
  const { flow } = await record(
    SAMPLE_APP,
    "secrets-opt-in",
    async (page) => {
      await page
        .locator("#password")
        .pressSequentially("hunter2", { delay: 5 });
    },
    { includeSecrets: true },
  );
  assert.deepEqual(humanSteps(flow), [["fill", "#password", "hunter2"]]);
  assert.deepEqual(flow.secrets, []);
});

/** A session with no browser behind it, so a step's anchor can be compared against an
 *  EXACT event timestamp rather than against a clock that keeps moving. */
function pageEmitting(
  drive: (emit: (event: CapturedEvent) => void) => void,
): DriverSession {
  const mainFrame = { url: () => "http://app.test/" };
  const page = {
    mainFrame: () => mainFrame,
    context: () => ({ addInitScript: async () => {} }),
    frames: () => [mainFrame],
    on: () => {},
    off: () => {},
    exposeBinding: async (
      _name: string,
      fn: (source: unknown, json: string) => void,
    ) => {
      drive((event) => fn({ frame: mainFrame }, JSON.stringify(event)));
    },
    addInitScript: async () => {},
    goto: async () => {},
    evaluate: async (fn: unknown, arg?: unknown) => {
      const source = String(fn);
      if (/Date\.now\(\)/.test(source) && source.length < 60) return Date.now();
      // The drain asks for the document mark in ONE call, so the fake answers both
      // halves together — a fake that answered only one would hide the very tearing
      // the atomic read exists to prevent.
      if (Array.isArray(arg)) return { frameId: "f_fake", seq: 0 };
      return { url: "http://app.test/", nodes: [] };
    },
    url: () => "http://app.test/",
  } as unknown as Page;
  return {
    sessionId: "fake",
    page,
    fetchReplay: async () => [],
    close: async () => {},
  };
}

test("a step's anchor IS the event's own timestamp, to the millisecond", async () => {
  // The binding hop costs milliseconds, so "now on arrival" is always later than the
  // interaction. Comparing against the exact stamp is the only assertion the hop's own
  // width cannot make pass by accident.
  const HOP_MS = 250;
  let stampedAt = 0;
  const session = pageEmitting((emit) => {
    setTimeout(() => {
      stampedAt = Date.now();
      const click: CapturedEvent = {
        kind: "click",
        frameId: "f_fake",
        seq: 1,
        timestamp: stampedAt,
        url: "http://app.test/",
        state: { url: "http://app.test/", nodes: [] },
        target: { selector: "#go", role: "button", name: "Go" },
        tagName: "button",
      };
      setTimeout(() => emit(click), HOP_MS);
    }, 20);
  });
  const flow = await recordFlow({
    session,
    specName: "anchored",
    driverName: "fake",
    startUrl: "http://app.test/",
    stop: new Promise<void>((resolve) => setTimeout(resolve, HOP_MS + 200)),
  });
  const step = flow.steps[1];
  assert.equal(step.action, "click");
  assert.equal(
    step.startedAt,
    stampedAt,
    `the step must open where the page stamped the click, not ${step.startedAt - stampedAt}ms later where the host heard it`,
  );
});

test("a step is anchored where the PAGE stamped it, not where the host heard it", async () => {
  // The binding hop is a process boundary. A window opened on arrival starts after the
  // interaction that caused it, so the interaction files in the PREVIOUS step's
  // window — the wrong-evidence-window class, in the capture domain.
  const recorded = await record(SAMPLE_APP, "anchoring", async (page) => {
    await page.click("#email");
    await page.click("#password");
  });
  const steps = recorded.flow.steps;
  const events = recorded.events ?? [];
  const segments = sliceSegments(events, steps);
  for (const [index, step] of steps.entries()) {
    if (step.action === "goto") continue;
    const segment = segments[index];
    assert.ok(
      segment.events.length > segment.preambleCount,
      `step ${step.index} (${step.action}) sliced nothing but pre-roll — its own interaction landed in another step's window`,
    );
  }
});

test("stopping drains what the page already sent, and records nothing after", async () => {
  // Two halves of one edge. Before the barrier, stop resolved and an empty pump returned
  // undefined, so an event still crossing the binding was queued with no consumer — the
  // human's last action vanished, and the test suite's own 200ms sleep was the only
  // thing hiding it. And after stop the listeners were still armed on the page, so a
  // session the caller went on using kept feeding a recording that had ended.
  const pages = pagesWith({
    "index.html": `<h1>Barrier</h1><button id="last">Last</button>
      <button id="after">After</button>
      <a id="leave" href="second.html">Leave</a>`,
    "second.html": "<h1>Second</h1>",
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  let stopNow = () => {};
  const stop = new Promise<void>((resolve) => {
    stopNow = resolve;
  });
  // The hop, made slow ON PURPOSE and deterministically. Its real latency varies, so a
  // test that happens to win the race proves nothing about the barrier — it proves the
  // machine was fast that time. Delivery is held back here by longer than the stop takes
  // to resolve, which is exactly the condition the barrier exists for.
  delayBindingDelivery(session.page, 250);
  const rawBinding: string[] = [];
  tapBinding(session.page, rawBinding);
  try {
    const flowing = recordFlow({
      session,
      specName: "barrier",
      driverName: driver.name,
      startUrl: `${app.baseUrl}/`,
      stop,
    });
    await session.page.waitForURL(`${app.baseUrl}/**`);
    await session.page.click("#last");
    stopNow();
    const flow = await flowing;
    assert.deepEqual(
      humanSteps(flow).map(([action, target]) => [action, target]),
      [["click", "#last"]],
      "the last action must land even when the stop follows it immediately",
    );

    // Asserted on the CHANNEL, not on the finished flow: nothing consumes the pump once
    // the recording has returned, so a flow that cannot grow proves nothing about
    // whether the page is still talking. What must be true is that the page stopped.
    const sentBeforeStop = rawBinding.length;
    await session.page.click("#after");
    await session.page.click("#leave");
    await session.page.waitForURL("**/second.html");
    await session.page.waitForTimeout(400);
    assert.equal(
      rawBinding.length,
      sentBeforeStop,
      `a stopped recording must leave NOTHING on the page — it sent ${rawBinding.length - sentBeforeStop} more event(s) after the stop`,
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("the drain waits per DOCUMENT — a sequence restarts, a barrier must not", async () => {
  // Event numbering restarts in every document, but the barrier tracked ONE global
  // maximum. After a busy first page, the number already delivered was larger than
  // anything the new page could have reached, so the stop condition was satisfied before
  // the new document's own event arrived — and the human's last action on the page they
  // ended on was the one thing the barrier existed to save.
  const pages = pagesWith({
    "index.html": `<h1>Busy</h1><button id="tick">Tick</button>
      <a id="go" href="next.html">Next</a>`,
    "next.html": '<h1>Quiet</h1><button id="last">Last</button>',
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  let stopNow = () => {};
  const stop = new Promise<void>((resolve) => {
    stopNow = resolve;
  });
  delayBindingDelivery(session.page, 250);
  try {
    const flowing = recordFlow({
      session,
      specName: "per-document-drain",
      driverName: driver.name,
      startUrl: `${app.baseUrl}/`,
      stop,
    });
    await session.page.waitForURL(`${app.baseUrl}/**`);
    // Run the first document's counter well past anything the second will reach.
    for (let tick = 0; tick < 12; tick += 1) {
      await session.page.click("#tick");
    }
    await session.page.click("#go");
    await session.page.waitForURL("**/next.html");
    await session.page.click("#last");
    stopNow();
    const flow = await flowing;
    const targets = humanSteps(flow).map(([, target]) => target);
    assert.ok(
      targets.includes("#last"),
      `the last action on the LAST document must land — saw ${JSON.stringify(targets)}`,
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("a session can be recorded TWICE — a stopped recording leaves nothing behind", async () => {
  // `exposeBinding` cannot be undone and refuses a second registration, and the page
  // listeners were anonymous, so a stopped recording kept feeding a dead pump and the
  // session could never be recorded again.
  const pages = pagesWith({
    "index.html": '<h1>Twice</h1><button id="one">One</button>',
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  const runOnce = async (specName: string): Promise<RecordedFlow> => {
    let stopNow = () => {};
    const stop = new Promise<void>((resolve) => {
      stopNow = resolve;
    });
    const flowing = recordFlow({
      session,
      specName,
      driverName: driver.name,
      startUrl: `${app.baseUrl}/`,
      stop,
    });
    await session.page.waitForURL(`${app.baseUrl}/**`);
    await session.page.click("#one");
    // No grace sleep here either — the stop barrier is what lands the click.
    stopNow();
    return flowing;
  };
  try {
    const first = await runOnce("first");
    const second = await runOnce("second");
    for (const flow of [first, second]) {
      assert.deepEqual(humanSteps(flow), [["click", "#one", undefined]]);
    }
  } finally {
    await session.close();
    await app.close();
  }
});

test("a recorded flow's step log slices the SAME stream a scripted one does, and replays green", async () => {
  const recorded = await record(
    SAMPLE_APP,
    "recorded-approve-an-order",
    async (page) => {
      await page
        .locator("#email")
        .pressSequentially("ops@depot.test", { delay: 5 });
      await page
        .locator("#password")
        .pressSequentially("hunter2", { delay: 5 });
      await page.click("#signin-button");
      await page.click('[data-order-id="SO-4472"]');
      await page
        .locator("#note")
        .pressSequentially("verified against PO 88120", { delay: 2 });
      await page.click("#approve-button");
    },
    {
      includeSecrets: true,
      after: async ({ flow, events }) => {
        // Evidence first, while the session's stream is the one this recording produced.
        const segments = sliceSegments(events ?? [], flow.steps);
        assert.equal(segments.length, flow.steps.length);
        for (const segment of segments) {
          assert.ok(
            isRenderable(segment),
            `step ${segment.stepIndex} (${segment.stepId}) sliced an unrenderable segment`,
          );
        }
      },
    },
  );

  const spec = applyProposals(recorded.flow.spec, recorded.flow.proposals);
  // Round-trip: what would be committed is what a replay would load.
  const roundTripped = loadSpec(saveSpec(spec));
  assert.deepEqual(
    roundTripped.steps.map((step) => step.action),
    ["goto", "fill", "fill", "click", "click", "fill", "click"],
    "the human's seven actions must be seven steps",
  );

  const app = await serveDirectory(SAMPLE_APP);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const replayed = {
      ...roundTripped,
      startUrl: `${app.baseUrl}/`,
      steps: roundTripped.steps.map((step) =>
        step.action === "goto" ? { ...step, target: `${app.baseUrl}/` } : step,
      ),
    };
    const result = await replaySpec(replayed, session);
    assert.equal(
      result.outcome,
      "passed",
      `replay of the recorded spec failed: ${result.failure?.error ?? ""}`,
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("a fallback that does not single out its frame is REFUSED, never emitted as a chain", () => {
  // Same-name siblings: the owning-element proof fails (no id, no test id, and the name
  // attribute matches both), so the fallback would have emitted `{ name: "twin" }` for
  // BOTH frames — a chain replay resolves to whichever it meets first, misattributing
  // every action proven inside the other. A chain that cannot single out one frame is
  // not a chain.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1>
      <iframe name="twin" src="twin.html"></iframe>
      <iframe name="twin" src="twin.html"></iframe>
      <button id="done">Done</button>`,
    "twin.html": '<button id="inner">Inner</button>',
  });

  return (async () => {
    const { flow } = await record(pages, "twins", async (page) => {
      await page.frameLocator("iframe >> nth=1").locator("#inner").click();
      await page.click("#done");
    });

    assert.deepEqual(
      humanSteps(flow).map(([action, target]) => [action, target]),
      [["click", "#done"]],
      `an unaddressable frame's click must not become a step — saw ${JSON.stringify(humanSteps(flow))}`,
    );
    assert.ok(
      flow.warnings.some((line) =>
        /frame not uniquely addressable: 2 siblings share name "twin"/.test(
          line,
        ),
      ),
      `the refusal must name the ambiguity — saw ${JSON.stringify(flow.warnings)}`,
    );
  })();
});

test("an anonymous frame that NAVIGATES keeps recording — the chain is per document, not per Frame", async () => {
  // A `Frame` object outlives the document in it. Keyed by Frame, the cache handed back
  // the chain derived from /before for a document now at /after, and every later
  // interaction in that frame was dropped as unresolvable — the human's last click
  // vanished from the spec with only a warning to show for it.
  //
  // The navigation is driven from the HOST page on purpose: the chain is built when the
  // event reaches the host, so a frame that navigated itself would already be on the new
  // document by then and both chains would agree however the cache is keyed.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe src="before.html"></iframe>
      <button id="nav">Swap</button>
      <script>
        document.getElementById("nav").addEventListener("click", function () {
          document.querySelector("iframe").src = "after.html";
        });
      </script>`,
    "before.html": '<button id="early">Early</button>',
    "after.html": '<button id="late">Late</button>',
  });

  const { flow } = await record(pages, "renavigating-frame", async (page) => {
    await page.frameLocator("iframe").locator("#early").click();
    await page.click("#nav");
    await page.frameLocator("iframe").locator("#late").waitFor();
    await page.frameLocator("iframe").locator("#late").click();
  });

  const targets = humanSteps(flow).map(([action, target]) => [action, target]);
  assert.ok(
    targets.some(([, target]) => target === "#late"),
    `the click AFTER the frame navigated must still be recorded — saw ${JSON.stringify(targets)} ${JSON.stringify(flow.warnings)}`,
  );
  const early = flow.spec.steps.find((step) => step.target === "#early");
  const late = flow.spec.steps.find((step) => step.target === "#late");
  assert.match(JSON.stringify(early?.frame), /before\.html/);
  assert.match(
    JSON.stringify(late?.frame),
    /after\.html/,
    `the chain must name the document the frame is on NOW — saw ${JSON.stringify(late?.frame)}`,
  );
});

test("typing either side of a child navigation is TWO fills, not one", async () => {
  // The same `#code` exists in both documents of one frame, and the frame navigates
  // itself: NO host action separates the two entries, so nothing outside the recorder's
  // own reading of the event stream can end the coalescing.
  //
  // What ends it here is the child's own load announcement, which is a document boundary
  // by construction. The document-id rule that coalescing is keyed by is proved by the
  // sibling-frames test below, where two documents are live at once and no navigation
  // separates them; this one holds the outcome a human would notice — that the value they
  // typed first is still in the spec.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe id="child" src="first.html"></iframe>`,
    "first.html": `<input id="code" />
      <script>
        document.getElementById("code").addEventListener("input", function () {
          if (this.value.length === 2) location.replace("second.html");
        });
      </script>`,
    "second.html": '<input id="code" />',
  });

  const { flow } = await record(pages, "two-documents", async (page) => {
    await page.frameLocator("#child").locator("#code").fill("aa");
    await page.frameLocator("#child").locator("#code").waitFor();
    await page.frameLocator("#child").locator("#code").fill("bb");
  });

  const fills = flow.spec.steps.filter((step) => step.action === "fill");
  assert.equal(
    fills.length,
    2,
    `each document's typing is its own step — saw ${JSON.stringify(humanSteps(flow))} ${JSON.stringify(flow.warnings)}`,
  );
  assert.equal(fills[0].value, "aa");
  assert.equal(fills[1].value, "bb");
  // And the frame's own load never became a step: a `goto` navigates the page, and one
  // made of this would have driven the whole page to the frame's src.
  assert.deepEqual(
    flow.spec.steps
      .filter((step) => step.action === "goto")
      .map((step) => step.target),
    [flow.spec.startUrl],
    `only the recording's own goto — saw ${JSON.stringify(humanSteps(flow))}`,
  );
});

test("an iframe nobody touched does not hold the stop open to the drain deadline", async () => {
  // A child document's own load announcement was withheld from the pump, so its sequence
  // number was never counted — while the stop barrier, which reads every document's
  // high-water mark, waited for exactly that number. Every recording of a page with an
  // iframe in it took the full five-second deadline to stop.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe id="idle" src="idle.html"></iframe>
      <button id="done">Done</button>`,
    "idle.html": "<p>Nobody clicks here</p>",
  });

  const started = Date.now();
  const { flow } = await record(pages, "idle-iframe", async (page) => {
    await page.click("#done");
  });
  const elapsed = Date.now() - started;

  assert.deepEqual(
    humanSteps(flow).map(([action, target]) => [action, target]),
    [["click", "#done"]],
  );
  assert.ok(
    elapsed < 4000,
    `the stop must not wait on a document nothing was withheld from — took ${elapsed}ms of the 5000ms deadline`,
  );
});

test("stopping stops in EVERY document — a child frame's listeners come off too", async () => {
  // Teardown reached the main document only, while the SINK stays alive for the drain —
  // that is what the drain is for. A child frame therefore kept its listeners and kept
  // reporting into a live sink, so whatever the human did to that iframe after pressing
  // stop was recorded as if they had done it before.
  //
  // Driven against the listener seam directly, not through `record`: a post-stop click
  // scheduled from a test races the drain, which finishes in milliseconds, so a test
  // shaped that way passes against the bug by never reaching the window it is about.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe id="child" src="child.html"></iframe>`,
    "child.html": '<button id="inner">Inner</button>',
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const seen: CapturedEvent[] = [];
    const handle = await attachCaptureListeners(session.page, (event) =>
      seen.push(event),
    );
    await session.page.goto(`${app.baseUrl}/index.html`);
    await session.page.frameLocator("#child").locator("#inner").click();
    await session.page.waitForTimeout(200);
    const before = seen.filter((event) => event.kind === "click").length;
    assert.ok(before > 0, "the child must be listening before the stop");

    await handle.stopListening();
    await session.page.frameLocator("#child").locator("#inner").click();
    await session.page.waitForTimeout(200);
    assert.equal(
      seen.filter((event) => event.kind === "click").length,
      before,
      "a child frame must stop reporting the moment the human stops",
    );
    await handle.detach();
  } finally {
    await session.close();
    await app.close();
  }
});

test("an event is named from ITS OWN document, and refused rather than misattributed", async () => {
  // The naming of a nested document — its name, its URL, how many siblings share either —
  // was read off a LIVE frame, in a walk that runs after an await. A click reported while
  // the frame was on /before was named from whatever document the frame had reached by
  // the time the walk got to it.
  //
  // /after carries the same `#tap` button, so the misnamed chain RESOLVED and the locator
  // PROVED: a step recorded, well-formed, addressed to a document the human never touched
  // and asserting on an element they never clicked. Nothing downstream could tell.
  //
  // Named from its own document's snapshot, the chain points at /before, which is gone —
  // so the step is refused by name. Losing a step the human took is bad; recording one
  // they did not is worse, and it is the only outcome of the two that is silent.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe src="before.html"></iframe>`,
    "before.html": `<button id="tap">Tap</button>
      <script>
        document.getElementById("tap").addEventListener("click", function () {
          location.replace("after.html");
        });
      </script>`,
    "after.html": '<button id="tap">Tap</button>',
  });

  const { flow } = await record(pages, "queued-naming", async (page) => {
    await page.frameLocator("iframe").locator("#tap").click();
    await page.waitForTimeout(300);
  });

  const named = JSON.stringify(
    flow.spec.steps.map((step) => [step.target, step.frame]),
  );
  assert.ok(
    !named.includes("after.html"),
    `no step may be attributed to a document the human never touched — saw ${named}`,
  );
  assert.ok(
    flow.warnings.some((line) => line.includes("before.html")),
    `and the one they did touch is refused BY NAME — saw ${JSON.stringify(flow.warnings)}`,
  );
});

test("the naming facts are CAPTURED with the document, not read off the frame later", async () => {
  // The discriminating half of the rule above, at the seam and with no race in it: take
  // the snapshot while the frame is on /before, navigate the frame, and the snapshot must
  // still describe /before. Read live — which is what the walk used to do, after an await
  // — the same frame answers /after, and a queued event is then named for a document it
  // never came from.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe src="before.html"></iframe>`,
    "before.html": "<p>before</p>",
    "after.html": "<p>after</p>",
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/index.html`);
    const frame = session.page.frames()[1];
    const captured = framePathFacts(session.page, frame);
    assert.deepEqual(captured[0].fallback, {
      urlPrefix: `${app.baseUrl}/before.html`,
    });

    await session.page.evaluate(() => {
      (document.querySelector("iframe") as HTMLIFrameElement).src =
        "after.html";
    });
    await session.page.frameLocator("iframe").getByText("after").waitFor();

    assert.deepEqual(
      captured[0].fallback,
      { urlPrefix: `${app.baseUrl}/before.html` },
      "the captured facts must not follow the frame",
    );
    // The control: taken NOW, the same frame names the document it is on now. That is
    // what the walk used to see, and why a queued event was misnamed.
    assert.deepEqual(framePathFacts(session.page, frame)[0].fallback, {
      urlPrefix: `${app.baseUrl}/after.html`,
    });
  } finally {
    await session.close();
    await app.close();
  }
});

test("a document that opens DURING the drain never arms — nothing it sees enters the spec", async () => {
  // The sink stays open through the drain, deliberately: that is how the human's last
  // action lands. Teardown reaches the documents that are already here — but a document
  // that loads afterwards runs the bundle fresh, asks whether a recording is running, and
  // was told YES, because the answer was "is there a sink" rather than "is the human
  // still recording". It then armed and reported into that open sink.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe id="child" src="child.html"></iframe>
      <button id="done">Done</button>`,
    "child.html": '<button id="early">Early</button>',
    "late.html": '<button id="late">Late</button>',
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const seen: CapturedEvent[] = [];
    const handle = await attachCaptureListeners(session.page, (event) =>
      seen.push(event),
    );
    await session.page.goto(`${app.baseUrl}/index.html`);
    await session.page.frameLocator("#child").locator("#early").click();
    await session.page.waitForTimeout(200);
    const before = seen.filter((event) => event.kind === "click").length;
    assert.ok(before > 0, "the child must be listening before the stop");

    // The human stops. The sink is still open — the drain has not run yet.
    await handle.stopListening();
    await session.page.evaluate(() => {
      (document.querySelector("iframe") as HTMLIFrameElement).src = "late.html";
    });
    await session.page.frameLocator("#child").locator("#late").waitFor();
    await session.page.frameLocator("#child").locator("#late").click();
    await session.page.waitForTimeout(200);

    assert.equal(
      seen.filter((event) => event.kind === "click").length,
      before,
      "a document that opened after the stop must never arm",
    );
    await handle.detach();
  } finally {
    await session.close();
    await app.close();
  }
});

test("a SECOND recording waits on its own delivery, not the first recording's", async () => {
  // `exposeBinding` cannot be replaced, so the permanent handler holds whatever it closed
  // over — and it closed over the first recording's delivery chain. The second recording
  // awaited a fresh promise nothing was ever appended to, so `settleDelivery` returned at
  // once and the barrier read its marks with the human's last events still unqueued: a
  // click missing from the spec, silently, and only ever on a second recording.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1><iframe id="child" src="child.html"></iframe>
      <button id="host">Host</button>`,
    "child.html": '<button id="inner">Inner</button>',
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    for (const round of ["first", "second"] as const) {
      let stopNow = () => {};
      const stop = new Promise<void>((resolve) => {
        stopNow = resolve;
      });
      const flowing = recordFlow({
        session,
        specName: round,
        driverName: driver.name,
        startUrl: `${app.baseUrl}/index.html`,
        stop,
      });
      await session.page.waitForURL(`${app.baseUrl}/**`);
      await session.page.frameLocator("#child").locator("#inner").click();
      stopNow();
      const flow = await flowing;
      assert.deepEqual(
        humanSteps(flow).map(([action, target]) => [action, target]),
        [["click", "#inner"]],
        `the ${round} recording must land its click — saw ${JSON.stringify(humanSteps(flow))} ${JSON.stringify(flow.warnings)}`,
      );
    }
  } finally {
    await session.close();
    await app.close();
  }
});

test("a second recording gets its OWN state — the binding holds none of the first's", async () => {
  // `exposeBinding` cannot be replaced, so the permanent handler holds whatever it closed
  // over: the first recording's delivery chain, chain cache, document paths and refusal
  // set. Every one of those is per recording, and the visible half is the refusal set —
  // a warning is emitted once per frame, so a second recording of the same page went
  // silent about a frame it could not address, and the human read a spec missing an
  // action with nothing to say why.
  const pages = pagesWith({
    "index.html": `<h1>Host</h1>
      <iframe srcdoc="<button id='inner'>Inner</button>"></iframe>
      <button id="done">Done</button>`,
  });
  const app = await serveDirectory(pages);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    for (const round of ["first", "second"] as const) {
      let stopNow = () => {};
      const stop = new Promise<void>((resolve) => {
        stopNow = resolve;
      });
      const flowing = recordFlow({
        session,
        specName: round,
        driverName: driver.name,
        startUrl: `${app.baseUrl}/index.html`,
        stop,
      });
      await session.page.waitForURL(`${app.baseUrl}/**`);
      await session.page.frameLocator("iframe").locator("#inner").click();
      await session.page.click("#done");
      stopNow();
      const flow = await flowing;
      assert.ok(
        flow.warnings.some((line) => /nested frame/.test(line)),
        `the ${round} recording must say what it could not address — saw ${JSON.stringify(flow.warnings)}`,
      );
    }
  } finally {
    await session.close();
    await app.close();
  }
});

/** A payload shaped like the bundle's, for the pump's own tests. The pump reads three
 *  fields off an event and nothing else. */
function pumped(frameId: string, seq: number): CapturedEvent {
  return {
    kind: "click",
    timestamp: 0,
    url: "http://app.test/",
    frameId,
    seq,
    state: { url: "http://app.test/", nodes: [] },
  };
}

test("the pump delivers what was in flight at the stop and discards what came after", async () => {
  // The gate, at the pump itself — the real one, with no browser in the way. The drain
  // exists to deliver what the page had ALREADY sent when the human stopped; anything
  // past a document's high-water mark was produced afterwards, and a document the barrier
  // never saw opened after the stop and has no mark to be past at all.
  let stopNow = () => {};
  const stop = new Promise<void>((resolve) => {
    stopNow = resolve;
  });
  const warnings: string[] = [];
  const pump = createEventPump(
    stop,
    async () => [{ frameId: "doc-a", seq: 2 }],
    (warning) => warnings.push(warning),
  );

  pump.push(pumped("doc-a", 1));
  stopNow();
  // Let the barrier read its marks before the late traffic arrives.
  await new Promise((resolve) => setTimeout(resolve, 10));
  pump.push(pumped("doc-a", 2)); // in flight at the stop — must land
  pump.push(pumped("doc-a", 3)); // produced after the stop — must not
  pump.push(pumped("doc-late", 1)); // a document the barrier never saw

  const delivered: [string, number][] = [];
  for (;;) {
    const event = await pump.next();
    if (event === undefined) break;
    delivered.push([event.frameId, event.seq]);
  }
  assert.deepEqual(delivered, [
    ["doc-a", 1],
    ["doc-a", 2],
  ]);
  assert.equal(warnings.length, 1, JSON.stringify(warnings));
  assert.match(warnings[0], /after the recording stopped/);
  assert.match(warnings[0], /opened during the drain/);
});

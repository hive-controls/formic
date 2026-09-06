/**
 * Locator derivation is the half of a recording that must be DETERMINISTIC — the human
 * supplies the clicks, the derivation supplies the addresses, and a derivation that
 * preferred a different field than the replay runner resolves with would write specs
 * whose locator is not the one the runner uses.
 *
 * Two things are pinned. First, the ORDER, against `LOCATOR_PRECEDENCE` itself rather
 * than a hand-copied list, so reordering the contract moves these expectations instead
 * of leaving them asserting the old one. Second, that the step's target string and the
 * step's assertion resolve THE SAME ELEMENT in a real browser when the page's own text
 * is hostile — the property string comparison cannot see, and the one an injection
 * defect breaks.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { LOCATOR_PRECEDENCE } from "../spec/locator-precedence.mts";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import { serveDirectory } from "../replay/sample-app-server.mts";
import { locatorFor } from "../replay/assertions.mts";
import type { Page } from "playwright-core";
import {
  captureBundleSource,
  readDocumentMark,
  preferredLocator,
  targetSelectorFor,
  CAPTURE_BINDING,
  CLASSIFY_HOOK,
  INSTALL_HOOK,
  TEARDOWN_HOOK,
  VISIBLE_STATE_HOOK,
  type TargetFacts,
} from "./events.mts";

const EVERY_FACT: TargetFacts = {
  testId: "current-user",
  selector: "#who",
  role: "button",
  name: "Sign in",
  text: "Sign in",
  cssFallback: "body > div > button",
};

test("derivation follows LOCATOR_PRECEDENCE — the field the runner would resolve with", () => {
  assert.deepEqual(LOCATOR_PRECEDENCE, ["testId", "selector", "role", "text"]);
  // Peel one field at a time: each round the winner must be the next field in the list.
  const facts: TargetFacts = { ...EVERY_FACT };
  for (const field of LOCATOR_PRECEDENCE) {
    const derived = preferredLocator(facts);
    assert.equal(
      derived[field],
      EVERY_FACT[field],
      `with ${field} present the derivation must use it`,
    );
    delete facts[field];
  }
  // Only once nothing else is reportable does the positional path win.
  assert.deepEqual(preferredLocator(facts), {
    selector: EVERY_FACT.cssFallback,
  });
});

test("a role+name outranks a positional CSS path — the reviewer's name, not a position", () => {
  // The defect this pins: `selectorOf` used to answer for EVERY element, so a
  // structural path was always present and role/text could never win. A button with an
  // accessible name and no id or testid must derive the ROLE form.
  const facts: TargetFacts = {
    role: "button",
    name: "Approve order",
    text: "Approve order",
    cssFallback: "body > section:nth-of-type(3) > button:nth-of-type(2)",
  };
  assert.deepEqual(preferredLocator(facts), {
    role: "button",
    name: "Approve order",
    exact: true,
  });
});

test("an arbitrary data attribute ranks with the fallback, never above role or text", () => {
  const facts: TargetFacts = {
    role: "link",
    name: "Back to orders",
    cssFallback: '[data-action="back"]',
  };
  assert.equal(preferredLocator(facts).role, "link");
});

test("a role locator carries its accessible name and asks for an exact match", () => {
  assert.deepEqual(preferredLocator({ role: "button", name: "Sign in" }), {
    role: "button",
    name: "Sign in",
    exact: true,
  });
  assert.deepEqual(preferredLocator({ role: "button" }), { role: "button" });
});

test("a target with no reportable locator is refused, never guessed", () => {
  assert.throws(() => preferredLocator({}), /no locator could be derived/);
  assert.throws(() => preferredLocator({ selector: "" }), /no locator/);
  assert.throws(() => targetSelectorFor({}), /no step target could be derived/);
});

test("a control character anywhere in a locator value is refused BY NAME", () => {
  const newline = `Order\napproved`;
  assert.throws(
    () => preferredLocator({ text: newline }),
    /control character U\+000A/,
  );
  assert.throws(
    () => preferredLocator({ role: "button", name: newline }),
    /control character U\+000A/,
  );
  assert.throws(
    () => targetSelectorFor({ testId: newline }),
    /control character U\+000A/,
  );
});

test("a step target is CSS with escaped attribute values — never interpolated engine syntax", () => {
  assert.equal(
    targetSelectorFor({ testId: "confirmation" }),
    '[data-testid="confirmation"]',
  );
  assert.equal(
    targetSelectorFor({ testId: 'x"], body, [x="' }),
    '[data-testid="x\\"], body, [x=\\""]',
  );
  assert.equal(targetSelectorFor({ selector: "#email" }), "#email");
  // A role- or text-preferred element still addresses its ACTION by CSS: the role and
  // the name ride in the assertion, structured, where Playwright applies them itself.
  assert.equal(
    targetSelectorFor({
      role: "button",
      name: 'Approve "SO-4472"',
      cssFallback: "body > button",
    }),
    "body > button",
  );
});

test("a classification is decided ONCE and cannot be downgraded afterwards", async () => {
  // The defect this pins: sensitivity was re-decided on every event from whatever the
  // element looked like at that moment. A reveal toggle flips `type` from password to
  // text, and the very next keystroke was recorded verbatim — the same field, the same
  // secret, withheld on one event and written down on the next.
  // Served rather than `setContent`: the bundle refuses to arm on about:blank, which is
  // where `setContent` leaves the document's own location.
  const directory = mkdtempSync(join(tmpdir(), "formic-classify-"));
  writeFileSync(
    join(directory, "index.html"),
    `<!doctype html><meta charset="utf-8"><input id="p" type="password"><input id="n" type="text" name="orderId">`,
  );
  const app = await serveDirectory(directory);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/`);
    await session.page.evaluate(captureBundleSource());
    const read = (selector: string) =>
      session.page.evaluate(
        ([hook, target]) =>
          (
            globalThis as unknown as Record<
              string,
              (element: Element | null) => unknown
            >
          )[hook](document.querySelector(target)),
        [CLASSIFY_HOOK, selector] as [string, string],
      );
    assert.deepEqual(await read("#p"), {
      category: "password",
      source: "type",
      evidence: "password",
    });
    assert.equal(await read("#n"), null);
    await session.page.evaluate(() => {
      document.querySelector("#p")?.setAttribute("type", "text");
      document.querySelector("#n")?.setAttribute("name", "cardNumber");
    });
    assert.deepEqual(
      await read("#p"),
      { category: "password", source: "type", evidence: "password" },
      "a revealed password field is still a password field",
    );
    assert.equal(
      await read("#n"),
      null,
      "and the decision is write-once in BOTH directions — it is a decision, not a poll",
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("the injected bundle names every hook it will actually use", () => {
  const source = captureBundleSource();
  for (const hook of [
    CAPTURE_BINDING,
    CLASSIFY_HOOK,
    VISIBLE_STATE_HOOK,
    INSTALL_HOOK,
    TEARDOWN_HOOK,
  ]) {
    assert.ok(source.includes(hook), `the bundle must publish ${hook}`);
  }
  assert.ok(
    !/__[A-Z_]+__/.test(source),
    "no placeholder may survive into the page",
  );
});

test("the bundle carries the secrets decision the host made, not a default", () => {
  assert.match(captureBundleSource(), /__formicCaptureSecrets = false/);
  assert.match(
    captureBundleSource({ includeSecrets: true }),
    /__formicCaptureSecrets = true/,
  );
});

test("a hostile test id resolves the SAME element through the target and the assertion", async () => {
  // The proof string comparison cannot give: an unescaped `"` used to close the
  // attribute early, so the recorded action resolved BODY while the assertion resolved
  // the button. Both must land on the button, in a real engine.
  const hostile = 'x"], body, [x="';
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.setContent(
      `<button data-testid='${hostile}'>Approve</button>`,
    );
    const facts: TargetFacts = {
      testId: hostile,
      cssFallback: "body > button",
    };
    const byTarget = session.page.locator(targetSelectorFor(facts));
    const byAssertion = locatorFor(session.page, preferredLocator(facts));
    assert.equal(await byTarget.count(), 1, "the step target must be unique");
    assert.equal(
      await byTarget.evaluate((node: Element) => node.tagName),
      "BUTTON",
    );
    assert.equal(
      await byAssertion.evaluate((node: Element) => node.tagName),
      "BUTTON",
    );
  } finally {
    await session.close();
  }
});

test("the drain's document mark is read ATOMICALLY, never torn across a navigation", async () => {
  // Document id and sequence were read in two round trips. A navigation landing between
  // them paired the OLD document's id with the NEW document's low sequence — a mark no
  // document ever had — and the barrier, satisfied by a count the old document passed
  // long ago, ended the drain while the new document's events were still outstanding.
  // One call cannot tear: whichever document answers, both halves come from it.
  let calls = 0;
  const navigatingPage = {
    // The first evaluate is answered by the old document; anything after it by the new
    // one, exactly as a navigation between two reads would.
    evaluate: async (fn: unknown, arg: unknown) => {
      calls += 1;
      const document =
        calls === 1
          ? { frame: "doc-old", seq: 12 }
          : { frame: "doc-new", seq: 1 };
      const globals = globalThis as unknown as Record<string, () => unknown>;
      const names = Array.isArray(arg) ? (arg as string[]) : [arg as string];
      const saved = names.map((name) => globals[name]);
      globals[names[0]] = () => document.frame;
      if (names[1] !== undefined) globals[names[1]] = () => document.seq;
      // Some callers read the seq hook through the first name; keep both bound.
      try {
        return await (fn as (a: unknown) => unknown)(arg);
      } finally {
        names.forEach((name, at) => {
          if (saved[at] === undefined) delete globals[name];
          else globals[name] = saved[at];
        });
      }
    },
  } as unknown as Page;

  const mark = await readDocumentMark(navigatingPage);

  assert.equal(calls, 1, "one round trip, so there is no window to tear in");
  assert.deepEqual(
    mark,
    { frameId: "doc-old", seq: 12 },
    `both halves must come from ONE document — saw ${JSON.stringify(mark)}`,
  );
});

test("the injected bundle's whitespace regex survives stringification", () => {
  // The bundle is built as a template string, so `\s` written in the source is just the
  // letter s by the time it reaches the page. Two of these lost their backslash and
  // became /s+/g — a regex that strips the letter s from a name. "Orders" normalised to
  // "Order", so a colliding heading no longer looked like a collision, and the ambiguous
  // locator this whole check exists to refuse got claimed instead.
  const source = captureBundleSource();
  assert.ok(
    source.includes("\\s+"),
    "the injected source must carry a real whitespace class",
  );
  assert.ok(
    !/replace\(\/s\+\/g/.test(source),
    "no /s+/g may reach the page — that matches the LETTER s",
  );
});

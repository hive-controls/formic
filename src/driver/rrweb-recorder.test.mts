/**
 * The replay stream is a SECOND channel out of the page, and it carries input values.
 *
 * rrweb's own default masks passwords and nothing else, so an email address, a card
 * number or a home address typed during a recording crossed a binding the capture
 * redaction never touched and accumulated in host memory — and then in an evidence
 * bundle attached to a pull request. The recording's classification is the only decision
 * about what is sensitive, and this is the projection of it onto the replay stream.
 *
 * Without a recording on the page there is no classifier, and the stream must be no
 * weaker than rrweb's own default: passwords still masked.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { LocalPlaywrightDriver } from "./local-playwright.mts";
import { serveDirectory } from "../replay/sample-app-server.mts";
import { attachCaptureListeners } from "../capture/events.mts";
import { replaySpec } from "../replay/runner.mts";
import type { DriverSession } from "./types.mts";

const FORM = `<h1>Checkout</h1>
  <label for="card">Card number</label><input id="card" type="text">
  <label for="note">Approval note</label><input id="note" type="text">
  <input id="secret" type="password">`;

function servePage(html: string): Promise<{
  baseUrl: string;
  close: () => Promise<void>;
}> {
  const directory = mkdtempSync(join(tmpdir(), "formic-rrweb-"));
  writeFileSync(
    join(directory, "index.html"),
    `<!doctype html><meta charset="utf-8">${html}`,
  );
  return serveDirectory(directory);
}

/** Everything the replay stream carries, as one string to search. */
async function streamText(session: DriverSession): Promise<string> {
  return JSON.stringify(await session.fetchReplay());
}

test("the replay stream masks every classified field, not passwords alone", async () => {
  const app = await servePage(FORM);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    const capture = await attachCaptureListeners(session.page, () => {});
    await session.page.goto(`${app.baseUrl}/`);
    await session.page
      .locator("#card")
      .pressSequentially("4111111111111111", { delay: 3 });
    await session.page
      .locator("#secret")
      .pressSequentially("hunter2", { delay: 3 });
    await session.page
      .locator("#note")
      .pressSequentially("ship next week", { delay: 3 });
    await capture.detach();
    const stream = await streamText(session);
    assert.doesNotMatch(
      stream,
      /4111111111111111/,
      "a card number must not reach the replay stream",
    );
    assert.doesNotMatch(stream, /hunter2/, "nor a password");
    assert.match(
      stream,
      /<secret:payment>/,
      "the stream says what was withheld, in the same vocabulary the spec uses",
    );
    assert.match(
      stream,
      /ship next week/,
      "an ordinary field is not masked — over-masking makes the evidence unreadable",
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("a REPLAY masks all seven categories too — evidence is not a lesser plane", async () => {
  // The classifier used to arrive only with a recording, so a replay — which is what
  // produces the evidence bundle attached to a pull request — fell back to rrweb's own
  // default and masked passwords alone. Every value a replay TYPES comes from a spec,
  // and a spec written with --include-secrets carries the real ones.
  const app = await servePage(FORM);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/`);
    // No capture listeners: this is the replay path exactly.
    await session.page.fill("#card", "4111111111111111");
    await session.page.fill("#note", "ship next week");
    const stream = await streamText(session);
    assert.doesNotMatch(
      stream,
      /4111111111111111/,
      "a card number typed during a REPLAY must not reach the evidence stream",
    );
    assert.match(stream, /<secret:payment>/);
    assert.match(
      stream,
      /ship next week/,
      "and an ordinary field stays readable",
    );
  } finally {
    await session.close();
    await app.close();
  }
});

test("with no recording on the page the stream still masks every classified field", async () => {
  const app = await servePage(FORM);
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  try {
    await session.page.goto(`${app.baseUrl}/`);
    await session.page
      .locator("#secret")
      .pressSequentially("hunter2", { delay: 3 });
    await session.page
      .locator("#note")
      .pressSequentially("ship next week", { delay: 3 });
    const stream = await streamText(session);
    assert.doesNotMatch(
      stream,
      /hunter2/,
      "a session with no recorder attached still masks passwords",
    );
    assert.match(stream, /ship next week/);
  } finally {
    await session.close();
    await app.close();
  }
});

/**
 * A REFERENCED value has no field classification to protect it.
 *
 * The classifier answers "will this field hold something sensitive" from what the page
 * declared about it — a type, an autocomplete token, the words in its label. A spec that
 * says `valueFrom` has already answered a different and stronger question: whoever wrote
 * it decided this value must not be written down at all, whatever the field looks like.
 * An unlabelled `<input type="text">` is the case the page-side routes all miss, and the
 * replay stream is the one plane the host-side scrub cannot reach reliably (rrweb splits
 * typed text across events, so an exact-token match does not fire).
 *
 * So replay MARKS the target before it fills it, in the classifier's own write-once memo
 * table — the same record every other projection reads — and rrweb masks it.
 */
test("a referenced fill into an unlabelled text input never reaches the replay stream", async () => {
  const app = await servePage(
    `<h1>Sign in</h1><input id="anonymous" type="text">`,
  );
  const driver = new LocalPlaywrightDriver();
  const session = await driver.open();
  const canary = "canary-secret-value";
  process.env.RRWEB_MASK_PASSWORD = canary;
  try {
    const result = await replaySpec(
      {
        name: "referenced",
        startUrl: `${app.baseUrl}/`,
        steps: [
          {
            id: "st_1",
            index: 1,
            action: "goto",
            target: `${app.baseUrl}/`,
          },
          {
            id: "st_2",
            index: 2,
            action: "fill",
            target: "#anonymous",
            valueFrom: "env.RRWEB_MASK_PASSWORD",
            assert: { selector: "#anonymous", visible: true },
          },
        ],
      },
      session,
      { stepTimeoutMs: 4000 },
    );
    assert.equal(result.outcome, "passed", JSON.stringify(result.failure));
    const stream = await streamText(session);
    // The control: this field is invisible to every classification route, so without
    // the mark the stream carries what was typed.
    assert.equal(
      stream.includes(canary),
      false,
      "the replay stream carries the resolved value",
    );
    assert.match(stream, /&lt;secret:referenced&gt;|<secret:referenced>/);
  } finally {
    delete process.env.RRWEB_MASK_PASSWORD;
    await session.close();
    await app.close();
  }
});

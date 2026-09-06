/**
 * One order, three consumers. The point of giving the list a home of its own was that a
 * second copy cannot drift out of step with the first — so what is pinned here is that
 * the consumers still RESOLVE in this order, behaviourally, not that some constant
 * deep-equals another.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import type { Page } from "playwright-core";
import {
  LOCATOR_PRECEDENCE,
  controlCharacterIn,
  escapeCssAttributeValue,
} from "./locator-precedence.mts";
import { locatorFor } from "../replay/assertions.mts";
import { locatorFormOf } from "../export/locators.mts";
import type { Assertion } from "./types.mts";

const EVERY_FIELD: Assertion = {
  testId: "t",
  selector: "#s",
  role: "button",
  name: "n",
  text: "x",
};

/** Reports which locator method the runner reached for, and nothing else. */
function recordingPage(reached: string[]): Page {
  const note = (field: string) => () => {
    reached.push(field);
    return {} as unknown;
  };
  return {
    getByTestId: note("testId"),
    locator: note("selector"),
    getByRole: note("role"),
    getByText: note("text"),
  } as unknown as Page;
}

test("the runner resolves in LOCATOR_PRECEDENCE order, field by field", () => {
  const assertion: Assertion = { ...EVERY_FIELD };
  for (const field of LOCATOR_PRECEDENCE) {
    const reached: string[] = [];
    locatorFor(recordingPage(reached), assertion);
    assert.deepEqual(
      reached,
      [field],
      `with ${field} still present the runner must resolve with it`,
    );
    delete assertion[field];
  }
});

test("the export compiler picks the SAME field the runner resolves with", () => {
  const assertion: Assertion = { ...EVERY_FIELD };
  for (const field of LOCATOR_PRECEDENCE) {
    assert.equal(
      locatorFormOf(assertion),
      field,
      `export must translate the field the runner would resolve with (${field})`,
    );
    delete assertion[field];
  }
  assert.equal(locatorFormOf({}), null);
});

test("control characters are NAMED, not merely rejected", () => {
  assert.equal(controlCharacterIn("plain"), null);
  assert.equal(controlCharacterIn("two\nlines"), "U+000A");
  assert.equal(
    controlCharacterIn(`a${String.fromCharCode(0)}b`),
    "U+0000",
    "the null byte is the one a naive truthiness check would miss",
  );
});

test("a CSS attribute value's quotes and backslashes escape losslessly", () => {
  assert.equal(
    escapeCssAttributeValue('x"], body, [x="'),
    'x\\"], body, [x=\\"',
  );
  assert.equal(escapeCssAttributeValue("back\\slash"), "back\\\\slash");
  assert.equal(escapeCssAttributeValue("plain"), "plain");
});

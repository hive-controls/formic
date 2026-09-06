/**
 * Puppeteer emitter — a standalone `*.mjs` script for `puppeteer`.
 *
 * Puppeteer ships no expectation library, so the emitted script carries its own: a
 * `waitForSelector` for visibility (which is also the wait the text checks need) and
 * two tiny comparison helpers, emitted only when the spec actually uses them. The
 * text helpers collapse whitespace before comparing because that is what Playwright's
 * `toHaveText` does, and the export has to mean what the spec means.
 *
 * Exit status is the script's contract: it throws on the first failed step and closes
 * the browser in `finally`, so CI sees a non-zero exit and never a leaked browser.
 */
import {
  frameChainLiteral,
  isUnsupported,
  quote,
  withSelector,
  type Unsupported,
} from "../locators.mts";
import {
  commentText,
  valueCode,
  type FrameScope,
  type Target,
} from "../compiler.mts";
import type { FrameChain } from "../../spec/types.mts";

/** A step's value as Puppeteer code — the same environment read the Playwright target
 *  emits, paired with a helper that throws by name when the variable is unset. */
function value(step: Parameters<typeof valueCode>[0]): string {
  return valueCode(
    step,
    (variable) => `process.env.${variable} ?? missingEnv(${quote(variable)})`,
    quote,
  );
}

const MISSING_ENV = [
  "function missingEnv(name) {",
  '  throw new Error("missing environment variable " + name + " — this script reads a value from it");',
  "}",
  "",
];

const TEXT_OF = [
  // Absence is "not yet", not a failure: page.$eval rejects when nothing matches, and
  // replay re-resolves its locator until the timeout rather than giving up on the first
  // miss. Returning null keeps a rerender that briefly detaches the node from ending
  // the poll early.
  "async function textOf(scope, selector) {",
  "  try {",
  '    const raw = await scope.$eval(selector, (el) => el.textContent ?? "");',
  '    return raw.replace(/\\s+/g, " ").trim();',
  "  } catch {",
  "    return null;",
  "  }",
  "}",
  "",
  // Playwright's toHaveText/toContainText retry until the timeout; a single sample
  // right after the visibility wait fails on any element whose text settles late.
  // This polls to the same deadline so the export means what the spec means.
  "async function expectText(scope, selector, expected, predicate, label) {",
  "  const deadline = Date.now() + TIMEOUT_MS;",
  "  let actual = null;",
  "  for (;;) {",
  "    actual = await textOf(scope, selector);",
  "    if (actual !== null && predicate(actual, expected)) return;",
  "    if (Date.now() >= deadline) break;",
  "    await new Promise((resolve) => setTimeout(resolve, POLL_MS));",
  "  }",
  '  const seen = actual === null ? "no matching element" : JSON.stringify(actual);',
  '  const detail = seen + " vs " + JSON.stringify(expected);',
  '  const where = label + " failed for " + selector;',
  '  throw new Error(where + " after " + TIMEOUT_MS + "ms: " + detail);',
  "}",
  "",
];

const EXPECT_HAS_TEXT = [
  "async function expectHasText(scope, selector, expected) {",
  '  await expectText(scope, selector, expected, (a, e) => a === e, "hasText");',
  "}",
  "",
];

const EXPECT_CONTAINS_TEXT = [
  "async function expectContainsText(scope, selector, expected) {",
  '  await expectText(scope, selector, expected, (a, e) => a.includes(e), "containsText");',
  "}",
  "",
];

// `url`/`urlPrefix`/`urlPattern` assert the current page URL rather than an element,
// so this poller reads `page.url()` directly instead of taking a selector — otherwise
// the same poll-to-the-deadline shape as `expectText` above, for the same reason:
// Puppeteer has no built-in retrying expectation, and a single sample right after the
// triggering action would fail on a navigation that settles a moment late.
const EXPECT_URL = [
  "async function expectUrl(expected, predicate, label) {",
  "  const deadline = Date.now() + TIMEOUT_MS;",
  "  let actual = page.url();",
  "  for (;;) {",
  "    actual = page.url();",
  "    if (predicate(actual, expected)) return;",
  "    if (Date.now() >= deadline) break;",
  "    await new Promise((resolve) => setTimeout(resolve, POLL_MS));",
  "  }",
  '  const detail = JSON.stringify(actual) + " vs " + JSON.stringify(expected);',
  '  throw new Error(label + " failed after " + TIMEOUT_MS + "ms: " + detail);',
  "}",
  "",
];

const EXPECT_URL_EXACT = [
  "async function expectUrlExact(expected) {",
  '  await expectUrl(expected, (actual, exp) => actual === exp, "url");',
  "}",
  "",
];

const EXPECT_URL_PREFIX = [
  "async function expectUrlPrefix(expected) {",
  '  await expectUrl(expected, (actual, exp) => actual.startsWith(exp), "urlPrefix");',
  "}",
  "",
];

const EXPECT_URL_PATTERN = [
  "async function expectUrlPattern(source) {",
  '  await expectUrl(source, (actual, src) => new RegExp(src).test(actual), "urlPattern");',
  "}",
  "",
];

/**
 * Puppeteer's own frame walk. Same shape as the replay runner's (replay/frames.mts) and
 * the Playwright target's: the owning `<iframe>` element for a selector link, and the
 * PARENT's own children — never the whole page — for a named one.
 */
const FRAME_CHAIN = [
  "async function frameChain(chain) {",
  "  let current = page.mainFrame();",
  "  const deadline = Date.now() + TIMEOUT_MS;",
  "  for (const [position, link] of chain.entries()) {",
  "    let next = null;",
  "    for (;;) {",
  "      let matches = 0;",
  "      if (link.selector !== undefined) {",
  "        const elements = await current.$$(link.selector);",
  "        matches = elements.length;",
  "        next = matches === 1 ? await elements[0].contentFrame() : null;",
  "      } else {",
  "        const found = current.childFrames().filter((child) => {",
  "          const url = child.url();",
  "          if (link.name !== undefined) return child.name() === link.name;",
  "          if (link.url !== undefined) return url === link.url;",
  "          return url.startsWith(link.urlPrefix);",
  "        });",
  "        matches = found.length;",
  "        next = found.length === 1 ? found[0] : null;",
  "      }",
  "      if (matches > 1) {",
  '        throw new Error("frame chain link " + (position + 1) + " (" + JSON.stringify(link) + ") is ambiguous — " + matches + " frames match");',
  "      }",
  "      if (next !== null) break;",
  "      if (Date.now() >= deadline) break;",
  "      await new Promise((resolve) => setTimeout(resolve, POLL_MS));",
  "    }",
  "    if (next === null) {",
  '      throw new Error("frame chain link " + (position + 1) + " (" + JSON.stringify(link) + ") did not resolve");',
  "    }",
  "    current = next;",
  "  }",
  "  return current;",
  "}",
  "",
];
function puppeteerFrameScope(
  chain: FrameChain,
  name: string,
): FrameScope | Unsupported {
  const literal = frameChainLiteral(chain, "puppeteer");
  if (isUnsupported(literal)) return literal;
  return {
    setup: [`const ${name} = await frameChain(${literal});`],
    expression: name,
  };
}

export const puppeteerTarget: Target = {
  name: "puppeteer",
  extension: ".mjs",
  indent: "  ",
  pageExpression: "page",

  // A Puppeteer Frame carries the same command surface a Page does — click, type,
  // select, waitForSelector, $eval — so every emitter below simply names its scope and
  // is otherwise identical whether the step is in a frame or not.
  frames: {
    helper: (usage) => (usage.frames ? FRAME_CHAIN : []),
    scope: puppeteerFrameScope,
  },

  preamble: (spec, usage) => [
    "",
    'import puppeteer from "puppeteer";',
    "",
    `// ${commentText(spec.name)}`,
    "const TIMEOUT_MS = 5000;",
    "const POLL_MS = 100;",
    "",
    "const browser = await puppeteer.launch();",
    "const page = await browser.newPage();",
    "page.setDefaultTimeout(TIMEOUT_MS);",
    "",
    ...(usage.valueFrom ? MISSING_ENV : []),
    ...(usage.frames ? FRAME_CHAIN : []),
    ...(usage.hasText || usage.containsText ? TEXT_OF : []),
    ...(usage.hasText ? EXPECT_HAS_TEXT : []),
    ...(usage.containsText ? EXPECT_CONTAINS_TEXT : []),
    ...(usage.url || usage.urlPrefix || usage.urlPattern ? EXPECT_URL : []),
    ...(usage.url ? EXPECT_URL_EXACT : []),
    ...(usage.urlPrefix ? EXPECT_URL_PREFIX : []),
    ...(usage.urlPattern ? EXPECT_URL_PATTERN : []),
    "try {",
  ],
  postamble: () => ["} finally {", "  await browser.close();", "}"],

  actions: {
    goto: (s) =>
      `await page.goto(${quote(s.target as string)}, { waitUntil: "load" });`,
    click: (s, scope) =>
      withSelector(s, "puppeteer", (sel) => `await ${scope}.click(${sel});`),
    fill: (s, scope) =>
      withSelector(s, "puppeteer", (sel) =>
        [
          `await ${scope}.waitForSelector(${sel}, { visible: true });`,
          // Triple-click selects the field's contents and Backspace deletes them, which
          // fires the same input events Playwright's page.fill does and works on a
          // contenteditable element — assigning `el.value` does neither.
          `await ${scope}.click(${sel}, { clickCount: 3 });`,
          `await page.keyboard.press("Backspace");`,
          `await ${scope}.type(${sel}, ${value(s)});`,
        ].join("\n"),
      ),
    press: (s, scope) =>
      withSelector(s, "puppeteer", (sel) =>
        [
          `await ${scope}.focus(${sel});`,
          `await page.keyboard.press(${value(s)});`,
        ].join("\n"),
      ),
    select: (s, scope) =>
      withSelector(
        s,
        "puppeteer",
        (sel) => `await ${scope}.select(${sel}, ${value(s)});`,
      ),
    waitFor: (s, scope) =>
      withSelector(
        s,
        "puppeteer",
        (sel) => `await ${scope}.waitForSelector(${sel}, { visible: true });`,
      ),
  },

  assertions: {
    visible: (locator, _a, scope) =>
      `await ${scope}.waitForSelector(${locator}, { visible: true });`,
    hidden: (locator, _a, scope) =>
      `await ${scope}.waitForSelector(${locator}, { hidden: true });`,
    hasText: (locator, a, scope) =>
      `await expectHasText(${scope}, ${locator}, ${quote(a.hasText as string)});`,
    containsText: (locator, a, scope) =>
      `await expectContainsText(${scope}, ${locator}, ${quote(a.containsText as string)});`,
  },

  urlAssertions: {
    url: (a) => `await expectUrlExact(${quote(a.url as string)});`,
    urlPrefix: (a) => `await expectUrlPrefix(${quote(a.urlPrefix as string)});`,
    urlPattern: (a) =>
      `await expectUrlPattern(${quote(a.urlPattern as string)});`,
  },
};

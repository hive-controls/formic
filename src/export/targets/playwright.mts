/**
 * Playwright emitter — a `*.spec.ts` for `@playwright/test`.
 *
 * The reference target: the replay runner and `checkAssertion` already speak
 * Playwright, so every cell below is the same call the harness itself would make.
 * That makes this file the yardstick the other two targets are read against.
 */
import {
  anyFrameLookup,
  escapeRegExpLiteral,
  frameChainLiteral,
  isUnsupported,
  quote,
  stepSelector,
  withSelector,
  type Unsupported,
} from "../locators.mts";
import { valueCode, type FrameScope, type Target } from "../compiler.mts";
import type { FrameChain } from "../../spec/types.mts";

/** A step's value as Playwright code. A reference reads the environment and, when the
 *  variable is unset, calls a helper that throws by name — `process.env.X` alone is
 *  `string | undefined`, which neither typechecks in a consumer's repo nor fails
 *  anywhere near the step that needed it. */
function value(step: Parameters<typeof valueCode>[0]): string {
  return valueCode(
    step,
    (variable) => `process.env.${variable} ?? missingEnv(${quote(variable)})`,
    quote,
  );
}

/** Emitted only when the spec references a variable. Its message names the variable
 *  and nothing else — there is no value to describe. */
const MISSING_ENV = [
  "",
  "function missingEnv(name: string): never {",
  "  throw new Error(",
  "    `missing environment variable ${name} — this test reads a value from it`,",
  "  );",
  "}",
];

/**
 * The lookup helper, emitted only when a chain names a frame by `name`/`url`/`urlPrefix`.
 *
 * `frameLocator()` takes a SELECTOR and nothing else, so a chain that asks a parent for a
 * named child has no locator spelling at all. This walks the chain the way the replay
 * runner walks it (replay/frames.mts) — child lookups against the PARENT's own children,
 * never the whole page, so a chain whose links are not actually nested does not quietly
 * resolve to a frame somewhere else.
 */
const FRAME_CHAIN = [
  "",
  "const FRAME_TIMEOUT_MS = 5000;",
  "const FRAME_POLL_MS = 50;",
  "",
  "type FrameLink = { selector?: string; name?: string; url?: string; urlPrefix?: string };",
  "",
  "async function frameChain(page: Page, chain: FrameLink[]): Promise<Frame> {",
  "  let current: Frame = page.mainFrame();",
  "  const deadline = Date.now() + FRAME_TIMEOUT_MS;",
  "  for (const [position, link] of chain.entries()) {",
  "    let next: Frame | null = null;",
  "    for (;;) {",
  "      let matches = 0;",
  "      if (link.selector !== undefined) {",
  "        const locator = current.locator(link.selector);",
  "        matches = await locator.count();",
  "        if (matches === 1) {",
  "          const element = await locator.elementHandle();",
  "          next = element === null ? null : await element.contentFrame();",
  "        }",
  "      } else {",
  "        const found = current.childFrames().filter((child) => {",
  "          const url = child.url();",
  "          if (link.name !== undefined) return child.name() === link.name;",
  "          if (link.url !== undefined) return url === link.url;",
  "          return url.startsWith(link.urlPrefix as string);",
  "        });",
  "        matches = found.length;",
  "        next = found.length === 1 ? found[0] : null;",
  "      }",
  "      if (matches > 1) {",
  "        throw new Error(",
  "          `frame chain link ${position + 1} (${JSON.stringify(link)}) is ambiguous — ${matches} frames match`,",
  "        );",
  "      }",
  "      if (next !== null) break;",
  "      if (Date.now() >= deadline) break;",
  "      await new Promise((resolve) => setTimeout(resolve, FRAME_POLL_MS));",
  "    }",
  "    if (next === null) {",
  "      throw new Error(",
  "        `frame chain link ${position + 1} (${JSON.stringify(link)}) did not resolve`,",
  "      );",
  "    }",
  "    current = next;",
  "  }",
  "  return current;",
  "}",
];
/**
 * A chain of `<iframe>` selectors is the idiom, and it is also the stronger form: a
 * `FrameLocator` re-resolves lazily and auto-waits on every use, exactly as the replay
 * runner's own timeout-bounded resolution does. Anything else goes through the helper.
 */
function playwrightFrameScope(
  chain: FrameChain,
  name: string,
): FrameScope | Unsupported {
  if (!anyFrameLookup(chain)) {
    const selectors = chain.map((link) =>
      stepSelector(link.selector as string, "playwright"),
    );
    const refused = selectors.find(isUnsupported);
    if (refused !== undefined) return refused;
    const expression = (selectors as string[])
      .map((selector) => `.frameLocator(${selector})`)
      .join("");
    return { setup: [], expression: `page${expression}` };
  }
  const literal = frameChainLiteral(chain, "playwright");
  if (isUnsupported(literal)) return literal;
  return {
    setup: [`const ${name} = await frameChain(page, ${literal});`],
    expression: name,
  };
}

/**
 * The expression an action is performed on.
 *
 * Unscoped, it stays the plain `page.click(selector)` shape a reader expects and every
 * generated file already carries. A `frameLocator` scope has no such form — it exposes
 * locators only — so a frame-scoped step goes through `<scope>.locator(selector)`, which
 * a FrameLocator and a Frame both answer to.
 */
function act(scope: string, selector: string): string {
  return `${scope}.locator(${selector})`;
}

export const playwrightTarget: Target = {
  name: "playwright",
  extension: ".spec.ts",
  indent: "  ",
  pageExpression: "page",

  frames: {
    helper: (usage) => (usage.frameLookup ? FRAME_CHAIN : []),
    scope: playwrightFrameScope,
  },

  preamble: (spec, usage) => [
    "",
    'import { expect, test } from "@playwright/test";',
    ...(usage.frameLookup
      ? ['import type { Frame, Page } from "@playwright/test";']
      : []),
    ...(usage.valueFrom ? MISSING_ENV : []),
    ...(usage.frameLookup ? FRAME_CHAIN : []),
    "",
    `test(${quote(spec.name)}, async ({ page }) => {`,
  ],
  postamble: () => ["});"],

  // A step with no frame keeps the plain `page.<action>(selector, …)` shape a reader
  // expects; a frame-scoped one goes through `<scope>.locator(selector)`, the one form
  // a Page, a Frame and a FrameLocator all answer to.
  actions: {
    goto: (s) => `await page.goto(${quote(s.target as string)});`,
    click: (s, scope) =>
      withSelector(s, "playwright", (sel) =>
        scope === "page"
          ? `await page.click(${sel});`
          : `await ${act(scope, sel)}.click();`,
      ),
    fill: (s, scope) =>
      withSelector(s, "playwright", (sel) =>
        scope === "page"
          ? `await page.fill(${sel}, ${value(s)});`
          : `await ${act(scope, sel)}.fill(${value(s)});`,
      ),
    press: (s, scope) =>
      withSelector(s, "playwright", (sel) =>
        scope === "page"
          ? `await page.press(${sel}, ${value(s)});`
          : `await ${act(scope, sel)}.press(${value(s)});`,
      ),
    select: (s, scope) =>
      withSelector(s, "playwright", (sel) =>
        scope === "page"
          ? `await page.selectOption(${sel}, ${value(s)});`
          : `await ${act(scope, sel)}.selectOption(${value(s)});`,
      ),
    waitFor: (s, scope) =>
      withSelector(
        s,
        "playwright",
        (sel) => `await ${act(scope, sel)}.waitFor({ state: "visible" });`,
      ),
  },

  assertions: {
    visible: (locator) => `await expect(${locator}).toBeVisible();`,
    hidden: (locator) => `await expect(${locator}).toBeHidden();`,
    hasText: (locator, a) =>
      `await expect(${locator}).toHaveText(${quote(a.hasText as string)});`,
    containsText: (locator, a) =>
      `await expect(${locator}).toContainText(${quote(a.containsText as string)});`,
  },

  // `toHaveURL` does an EXACT string comparison for `url` (verified against the
  // installed 1.62.1 types: no baseURL is configured here, so no glob/relative
  // resolution applies). `urlPrefix`/`urlPattern` go through `new RegExp(...)` rather
  // than a bare `/regex/` literal, so a `/` in the value never has to be escaped.
  //
  // The validator requires `url`/`urlPrefix` to be absolute (http/https). That is what
  // makes this equivalent to a raw string compare REGARDLESS of whether a consuming
  // project's own Playwright config sets `baseURL`: per the WHATWG URL spec, `new
  // URL(value, base)` — what `toHaveURL` resolves a string argument through — only
  // ever consults `base` when `value` is relative. An absolute `value` is parsed on
  // its own, so `baseURL` is structurally never in play here, matching Cypress's
  // `cy.url().should("eq", ...)` and Puppeteer's raw `page.url() === value`.
  urlAssertions: {
    url: (a) => `await expect(page).toHaveURL(${quote(a.url as string)});`,
    urlPrefix: (a) =>
      `await expect(page).toHaveURL(new RegExp(${quote(`^${escapeRegExpLiteral(a.urlPrefix as string)}`)}));`,
    urlPattern: (a) =>
      `await expect(page).toHaveURL(new RegExp(${quote(a.urlPattern as string)}));`,
  },
};

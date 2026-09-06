/**
 * The translation tables — the whole locator vocabulary, per target, on one screen.
 *
 * A spec locator is one of four forms, resolved by the same precedence the replay
 * runner uses (`testId` > `selector` > `role` > `text`, replay/assertions.mts). Each
 * target expresses those forms differently, and some cannot express one at all. Both
 * facts are DATA here: a cell either emits code or names why the target refuses.
 * Refusing beats approximating — an exported test that means something subtly other
 * than the spec is the wrong artifact, and it fails silently, in the consumer's CI.
 *
 * What a cell emits differs by target, deliberately:
 *   playwright  a `Locator` expression        page.getByTestId("x")
 *   puppeteer   a selector STRING literal     "[data-testid=\"x\"]"
 *   cypress     a chainable expression        cy.get('[data-testid="x"]')
 * The per-target assertion tables in `targets/` consume whichever shape their target
 * produces, so the compiler never has to know the difference.
 */
import type { Assertion, FrameChain } from "../spec/types.mts";
import {
  LOCATOR_PRECEDENCE,
  controlCharacterIn,
  escapeCssAttributeValue,
  type LocatorField,
} from "../spec/locator-precedence.mts";
import { FRAME_REF_FIELDS } from "../spec/types.mts";

export { controlCharacterIn };

export type TargetName = "playwright" | "puppeteer" | "cypress";

export const TARGET_NAMES: readonly TargetName[] = [
  "playwright",
  "puppeteer",
  "cypress",
];

/** The spec's four locator forms, in resolution order — spec/locator-precedence.mts
 *  owns both; this alias keeps the export tables reading in their own vocabulary. */
export type LocatorForm = LocatorField;

export { LOCATOR_PRECEDENCE };

/** The four predicates `checkAssertion` can apply. `visible`/`hidden` are the same
 *  field (`visible`), split here because they emit opposite expectations. */
export type AssertionKind = "visible" | "hidden" | "hasText" | "containsText";

/** A table cell's refusal: the reason, phrased for the person running the export. */
export interface Unsupported {
  readonly unsupported: string;
}

/** Emitted code, or a refusal. Every table cell returns one of these. */
export type Emitted = string | Unsupported;

export function isUnsupported(emitted: Emitted): emitted is Unsupported {
  return typeof emitted !== "string";
}

/**
 * A JavaScript string literal, quoted the way Prettier would quote it: double quotes
 * unless the value contains more double quotes than single ones. Generated code is
 * read by humans and committed to their repo, so it has to survive their formatter
 * without a reformat diff.
 */
export function quote(value: string): string {
  const doubles = (value.match(/"/g) ?? []).length;
  const singles = (value.match(/'/g) ?? []).length;
  const mark = doubles > singles ? "'" : '"';
  const escaped = value
    .replace(/\\/g, "\\\\")
    .replace(new RegExp(mark, "g"), `\\${mark}`)
    .replace(/\n/g, "\\n")
    .replace(/\r/g, "\\r")
    .replace(/[\u2028\u2029]/g, (character) =>
      character === "\u2028" ? "\\u2028" : "\\u2029",
    );
  return `${mark}${escaped}${mark}`;
}

/**
 * A CSS attribute selector whose value is escaped losslessly, or a refusal.
 *
 * `"` and `\` are the only two characters a double-quoted CSS attribute value cannot
 * carry raw, and both have a lossless backslash escape — so they are escaped, never
 * refused. Control characters have no such escape in this position, so a value
 * carrying one is refused BY NAME. Unescaped, a `"` closes the attribute early and the
 * rest of the value becomes selector syntax: `[data-testid="x"], body` would match the
 * whole page and quietly pass an assertion the spec never made.
 */
export function cssAttributeSelector(
  attribute: string,
  value: string,
  target: TargetName,
): Emitted {
  const control = controlCharacterIn(value);
  if (control !== null) {
    return {
      unsupported: `the ${attribute} ${quote(value)} contains the control character ${control}, which ${target} cannot carry in a CSS attribute selector`,
    };
  }
  return `[${attribute}="${escapeCssAttributeValue(value)}"]`;
}

/** Playwright selector syntax that is NOT CSS. A spec captured against Playwright may
 *  carry any of these in a step target or an `assert.selector`; every other target
 *  takes plain CSS and would silently match nothing. */
const PLAYWRIGHT_ONLY: ReadonlyArray<readonly [RegExp, string]> = [
  [/^[a-zA-Z_-]+=/, "a selector-engine prefix (`engine=`)"],
  // Playwright accepts XPath implicitly, by leading `//` or `(//`. CSS has no such
  // form, so an unrefused XPath would compile to a selector matching nothing.
  [/^\(*\/\//, "an implicit XPath selector"],
  [/>>/, "the `>>` engine chain"],
  [/:has-text\(/, "the `:has-text()` pseudo-class"],
  [/:text(-is|-matches)?\(/, "the `:text()` pseudo-class"],
  [/:visible|:hidden/, "the `:visible`/`:hidden` pseudo-class"],
  [/:nth-match\(/, "the `:nth-match()` pseudo-class"],
  [/:(above|below|left-of|right-of|near)\(/, "a layout pseudo-class"],
];

/** Names the Playwright-only construct in `selector`, or null when it is plain CSS. */
export function playwrightOnlyConstruct(selector: string): string | null {
  for (const [pattern, name] of PLAYWRIGHT_ONLY) {
    if (pattern.test(selector)) return name;
  }
  return null;
}

/** A CSS selector literal for a target that speaks only CSS, or a refusal naming the
 *  Playwright construct that stopped it. */
export function cssSelector(selector: string, target: TargetName): Emitted {
  const construct = playwrightOnlyConstruct(selector);
  if (construct !== null) {
    return {
      unsupported: `the selector ${quote(selector)} uses ${construct}, which ${target} cannot express`,
    };
  }
  return quote(selector);
}

/** step `target` -> per-target selector literal. Playwright takes its own selector
 *  syntax verbatim; every other target speaks only CSS and refuses the rest. */
const STEP_SELECTOR_TABLE: Record<TargetName, (selector: string) => Emitted> = {
  playwright: (selector) => quote(selector),
  puppeteer: (selector) => cssSelector(selector, "puppeteer"),
  cypress: (selector) => cssSelector(selector, "cypress"),
};

/** The step `target` as this target's locator: a URL for `goto` is passed through by
 *  the action tables, so this only ever sees an element selector. */
export function stepSelector(selector: string, target: TargetName): Emitted {
  return STEP_SELECTOR_TABLE[target](selector);
}

/** Builds an action statement from a step's translated target, propagating a refusal
 *  rather than emitting a command that would match nothing. */
export function withSelector(
  step: { target?: string },
  target: TargetName,
  build: (selector: string) => string,
): Emitted {
  const selector = stepSelector(step.target as string, target);
  return isUnsupported(selector) ? selector : build(selector);
}

/** Whether any link in this chain asks a parent for a NAMED child rather than pointing
 *  at the owning `<iframe>` element. Playwright's `frameLocator()` takes a selector and
 *  nothing else, so this is the question of whether the lookup helper is needed. */
export function anyFrameLookup(chain: FrameChain): boolean {
  return chain.some((link) => link.selector === undefined);
}

/**
 * A frame chain as an array literal for an emitted helper call, or a refusal.
 *
 * A frame link's `selector` addresses an `<iframe>` in a document, which is the same job
 * a step's `target` does — so it goes through the SAME per-target table. It used to be
 * quoted and emitted unexamined, and `xpath=//iframe` compiled into a plain-CSS lookup
 * (`current.$(...)`, `cy.get(...)`) that matches nothing: a chain that resolves no frame
 * at run time, from a compiler whose whole contract is to refuse what a target cannot
 * say. `name`/`url`/`urlPrefix` are compared as strings and need no such check.
 */
export function frameChainLiteral(
  chain: FrameChain,
  target: TargetName,
): Emitted {
  const links: string[] = [];
  for (const link of chain) {
    const [field] = FRAME_REF_FIELDS.filter(
      (candidate) => link[candidate] !== undefined,
    );
    if (field !== "selector") {
      links.push(`{ ${field}: ${quote(link[field] as string)} }`);
      continue;
    }
    const selector = stepSelector(link.selector as string, target);
    if (isUnsupported(selector)) return selector;
    links.push(`{ selector: ${selector} }`);
  }
  return `[${links.join(", ")}]`;
}

/** Which form resolves this assertion. The validator guarantees at least one field. */
export function locatorFormOf(assertion: Assertion): LocatorForm | null {
  return (
    LOCATOR_PRECEDENCE.find((form) => assertion[form] !== undefined) ?? null
  );
}

/** Which predicates this assertion applies, in the order `checkAssertion` applies
 *  them. Visibility is always one of them — it is implied unless `visible: false`. */
export function assertionKindsOf(assertion: Assertion): AssertionKind[] {
  const kinds: AssertionKind[] = [
    assertion.visible === false ? "hidden" : "visible",
  ];
  if (assertion.hasText !== undefined) kinds.push("hasText");
  if (assertion.containsText !== undefined) kinds.push("containsText");
  return kinds;
}

/** The page-level predicates: `url`/`urlPrefix`/`urlPattern` assert the current page
 *  URL rather than an element, so they are emitted independently of the four element
 *  locator forms above and never need one — see `checkUrlAssertion` in
 *  `replay/assertions.mts`, the same split. */
export type UrlAssertionKind = "url" | "urlPrefix" | "urlPattern";

export const URL_ASSERTION_KINDS: readonly UrlAssertionKind[] = [
  "url",
  "urlPrefix",
  "urlPattern",
];

/** Which URL predicates this assertion carries, in the order `checkUrlAssertion`
 *  applies them. */
export function urlAssertionKindsOf(assertion: Assertion): UrlAssertionKind[] {
  return URL_ASSERTION_KINDS.filter((kind) => assertion[kind] !== undefined);
}

/** Regex metacharacters escaped so a plain string can be embedded inside a
 *  `new RegExp(...)` source and still match only itself — the same escape set
 *  `cypressTextLocator` below uses for an exact text match, minus `/` (no bare
 *  regex-literal delimiter is ever emitted for a URL; see the per-target tables). */
export function escapeRegExpLiteral(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function playwrightRoleOptions(assertion: Assertion): string {
  const exact = assertion.exact ?? false;
  return assertion.name === undefined
    ? `{ exact: ${exact} }`
    : `{ name: ${quote(assertion.name)}, exact: ${exact} }`;
}

/** Puppeteer's `::-p-text()` takes an unquoted, uncommaed argument. */
function puppeteerTextSelector(assertion: Assertion): Emitted {
  const text = assertion.text as string;
  if (assertion.exact === true) {
    return {
      unsupported:
        "an exact `text` assertion — puppeteer's `::-p-text()` matches a substring and has no whole-string mode",
    };
  }
  if (/[(),\\]/.test(text)) {
    return {
      unsupported: `the text ${quote(text)} contains a character puppeteer's \`::-p-text()\` argument cannot carry`,
    };
  }
  return quote(`::-p-text(${text})`);
}

function cypressTextLocator(assertion: Assertion): Emitted {
  const text = assertion.text as string;
  if (assertion.exact !== true) return `cy.contains(${quote(text)})`;
  // `/` closes a regex literal, so it escapes alongside the metacharacters — without
  // it, text like `a/b` emits `cy.contains(/^a/b$/)`, which is not valid JavaScript.
  const escaped = text
    .replace(/[/.*+?^${}()|[\]\\]/g, "\\$&")
    .replace(/[\u2028\u2029]/g, (character) =>
      character === "\u2028" ? "\\u2028" : "\\u2029",
    );
  return `cy.contains(/^${escaped}$/)`;
}

const NO_ROLE_LOCATOR: Unsupported = {
  unsupported:
    "a `role` assertion — this target has no first-party accessible-name locator, and approximating one with `[role=...]` would assert something the spec does not say",
};

/**
 * spec locator form -> per-target locator code. The whole table.
 */
export const LOCATOR_TABLE: Record<
  TargetName,
  Record<LocatorForm, (assertion: Assertion, root: string) => Emitted>
> = {
  playwright: {
    testId: (a, root) => `${root}.getByTestId(${quote(a.testId as string)})`,
    selector: (a, root) => `${root}.locator(${quote(a.selector as string)})`,
    role: (a, root) =>
      `${root}.getByRole(${quote(a.role as string)}, ${playwrightRoleOptions(a)})`,
    text: (a, root) =>
      `${root}.getByText(${quote(a.text as string)}, { exact: ${a.exact ?? false} })`,
  },
  puppeteer: {
    testId: (a) => {
      const selector = cssAttributeSelector(
        "data-testid",
        a.testId as string,
        "puppeteer",
      );
      return isUnsupported(selector) ? selector : quote(selector);
    },
    selector: (a) => cssSelector(a.selector as string, "puppeteer"),
    role: () => NO_ROLE_LOCATOR,
    text: puppeteerTextSelector,
  },
  cypress: {
    testId: (a) => {
      const selector = cssAttributeSelector(
        "data-testid",
        a.testId as string,
        "cypress",
      );
      return isUnsupported(selector) ? selector : `cy.get(${quote(selector)})`;
    },
    selector: (a) => {
      const css = cssSelector(a.selector as string, "cypress");
      return isUnsupported(css) ? css : `cy.get(${css})`;
    },
    role: () => NO_ROLE_LOCATOR,
    text: cypressTextLocator,
  },
};

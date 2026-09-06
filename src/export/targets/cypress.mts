/**
 * Cypress emitter — a `*.cy.js` spec.
 *
 * Cypress commands are queued rather than awaited, so the emitted body is a flat
 * chain inside one `it`. Two places where Cypress cannot say exactly what the spec
 * says, both handled here rather than hidden:
 *
 *  - `press` goes through `.type()`, whose key vocabulary is a closed set of braced
 *    tokens. The map below is the whole translation; a key outside it is refused, not
 *    guessed. `Tab` is absent from Cypress on purpose and therefore refused.
 *  - `visible: false` becomes `should("not.be.visible")`, which requires the element
 *    to EXIST and be hidden. Playwright's `toBeHidden` also passes when the element is
 *    gone. The narrower reading is the safe one — it can only fail where Playwright
 *    would pass, never pass where Playwright would fail.
 *
 * A referenced value carries `{ log: false }`: Cypress prints a command's arguments in
 * its own command log, and that log is rendered into the screenshots and the video a
 * run records. Suppressing the entry is the only place the export can keep a resolved
 * value out of the consumer's own artifacts.
 */
import {
  escapeRegExpLiteral,
  isUnsupported,
  quote,
  withSelector,
  type Emitted,
} from "../locators.mts";
import { valueCode, type Target } from "../compiler.mts";

/** A step's value as Cypress code. `Cypress.env` is where a Cypress project keeps a
 *  variable, so a reference reads it there rather than `process.env` — the browser
 *  process the commands run in has no environment of its own. */
function value(step: Parameters<typeof valueCode>[0]): string {
  return valueCode(
    step,
    (variable) =>
      `Cypress.env(${quote(variable)}) ?? missingEnv(${quote(variable)})`,
    quote,
  );
}

const MISSING_ENV = [
  "  function missingEnv(name) {",
  '    throw new Error("missing Cypress environment variable " + name + " — this spec reads a value from it");',
  "  }",
];

/** Playwright key name -> the Cypress `.type()` token. */
const CYPRESS_KEYS: Record<string, string> = {
  Enter: "{enter}",
  Escape: "{esc}",
  Backspace: "{backspace}",
  Delete: "{del}",
  ArrowUp: "{uparrow}",
  ArrowDown: "{downarrow}",
  ArrowLeft: "{leftarrow}",
  ArrowRight: "{rightarrow}",
  Home: "{home}",
  End: "{end}",
  PageUp: "{pageup}",
  PageDown: "{pagedown}",
  Insert: "{insert}",
};

/** `.type()` reads `{` as the start of a key token, so literal braces double up. */
function typed(text: string): string {
  return quote(text.replace(/\{/g, "{{}"));
}

function cypressKey(key: string): Emitted {
  // Own properties only: a key named `constructor` or `toString` would otherwise read
  // an inherited Object member and emit it, instead of refusing by name.
  const token = Object.hasOwn(CYPRESS_KEYS, key)
    ? CYPRESS_KEYS[key]
    : undefined;
  if (token !== undefined) return quote(token);
  if ([...key].length === 1) return typed(key);
  return {
    unsupported: `the key ${quote(key)} — Cypress \`.type()\` has no token for it`,
  };
}

export const cypressTarget: Target = {
  name: "cypress",
  extension: ".cy.js",
  indent: "    ",
  pageExpression: "cy",

  // NO `frames` entry, deliberately. Cypress has no first-party command for a nested
  // browsing context: `cy.frameLoaded`/`cy.iframe` come from the `cypress-iframe`
  // plugin, a dependency a generated file cannot assume is installed, and the
  // hand-rolled `cy.get(sel).its("0.contentDocument")` idiom gives up the retrying
  // resolution every other command here has — an assertion inside a frame would then
  // pass or fail on when the frame happened to load. Both are approximations, and an
  // approximation that goes green in the consumer's CI is the failure this compiler
  // exists to refuse. A spec with a frame chain is refused for cypress BY NAME.

  preamble: (spec, usage) => [
    "",
    `describe(${quote(spec.name)}, () => {`,
    ...(usage.valueFrom ? MISSING_ENV : []),
    `  it(${quote(spec.name)}, () => {`,
  ],
  postamble: () => ["  });", "});"],

  actions: {
    goto: (s) => `cy.visit(${quote(s.target as string)});`,
    click: (s) =>
      withSelector(s, "cypress", (sel) => `cy.get(${sel}).click();`),
    // `.type()` reads `{` as the start of a key token, and a referenced value is not
    // known here to escape — so a reference turns that parsing OFF outright, which is
    // also the right reading for a credential: it is text, not a key sequence.
    fill: (s) =>
      withSelector(s, "cypress", (sel) =>
        s.valueFrom !== undefined
          ? `cy.get(${sel}).clear().type(${value(s)}, { parseSpecialCharSequences: false, log: false });`
          : s.value === ""
            ? `cy.get(${sel}).clear();`
            : `cy.get(${sel}).clear().type(${typed(s.value as string)});`,
      ),
    press: (s) => {
      // Cypress spells a key as a braced token chosen at GENERATION time, so a key
      // that only exists at run time cannot be translated — refused by name rather
      // than typed literally, which would send the key's spelling as text.
      if (s.valueFrom !== undefined) {
        return {
          unsupported: `a press whose key comes from ${s.valueFrom} — Cypress \`.type()\` needs a key token chosen when the file is generated`,
        };
      }
      const key = cypressKey(s.value as string);
      if (isUnsupported(key)) return key;
      return withSelector(
        s,
        "cypress",
        (sel) => `cy.get(${sel}).type(${key});`,
      );
    },
    select: (s) =>
      withSelector(s, "cypress", (sel) =>
        s.valueFrom !== undefined
          ? `cy.get(${sel}).select(${value(s)}, { log: false });`
          : `cy.get(${sel}).select(${value(s)});`,
      ),
    waitFor: (s) =>
      withSelector(
        s,
        "cypress",
        (sel) => `cy.get(${sel}).should("be.visible");`,
      ),
  },

  assertions: {
    visible: (locator) => `${locator}.should("be.visible");`,
    hidden: (locator) => `${locator}.should("not.be.visible");`,
    hasText: (locator, a) =>
      `${locator}.should("have.text", ${quote(a.hasText as string)});`,
    containsText: (locator, a) =>
      `${locator}.should("contain.text", ${quote(a.containsText as string)});`,
  },

  // `cy.url()` auto-retries like any other Cypress command; `.should("match", regex)`
  // needs a real RegExp, so `urlPrefix`/`urlPattern` go through `new RegExp(...)`
  // rather than a bare `/regex/` literal, exactly as the playwright target does.
  urlAssertions: {
    url: (a) => `cy.url().should("eq", ${quote(a.url as string)});`,
    urlPrefix: (a) =>
      `cy.url().should("match", new RegExp(${quote(`^${escapeRegExpLiteral(a.urlPrefix as string)}`)}));`,
    urlPattern: (a) =>
      `cy.url().should("match", new RegExp(${quote(a.urlPattern as string)}));`,
  },
};

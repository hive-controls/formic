/**
 * Spec load/save + validation.
 *
 * Validation exists because the format traded compile-time checking for reviewable
 * diffs (see types.mts). The validator is where that trade is paid back: a malformed
 * spec must fail loudly at load, never half-run.
 */
import { parse as parseYaml, stringify as stringifyYaml } from "yaml";
import {
  ACTION_KINDS,
  FRAME_REF_FIELDS,
  SECRET_PLACEHOLDER_PATTERN,
  VALUE_FROM_FORM,
  VALUE_FROM_PATTERN,
  type Spec,
  type SpecStep,
} from "./types.mts";

export class SpecValidationError extends Error {
  constructor(readonly problems: string[]) {
    super(`spec is invalid:\n  - ${problems.join("\n  - ")}`);
    this.name = "SpecValidationError";
  }
}

/** Actions that change application state, and therefore must carry an assertion.
 *  `waitFor` and `goto` are excluded: they assert by their own nature. */
const STATE_CHANGING = new Set(["click", "fill", "press", "select"]);

/** Every field an `Assertion` declares (types.mts). Kept as a closed set so a typo is a
 *  load error rather than a silently ignored key — see the unknown-field check below. */
const ASSERTION_FIELDS: ReadonlySet<string> = new Set([
  "testId",
  "selector",
  "role",
  "name",
  "text",
  "exact",
  "hasText",
  "containsText",
  "visible",
  "url",
  "urlPrefix",
  "urlPattern",
  "frame",
]);

/** `url`/`urlPrefix`/`urlPattern` assert the page; every other field asserts an
 *  element. The two never mix in one assert — see the mutual-exclusivity check below. */
const URL_ASSERTION_FIELDS: ReadonlySet<string> = new Set([
  "url",
  "urlPrefix",
  "urlPattern",
]);

/** `frame` is neither: it says WHERE an element field is resolved, so it is not itself
 *  an element predicate. Counting it as one would make a bare `{ frame }` read as an
 *  element assertion with no locator, and would report the wrong refusal for the
 *  `{ url, frame }` mistake — which has a refusal of its own, further down. */
const ELEMENT_ASSERTION_FIELDS: ReadonlySet<string> = new Set(
  [...ASSERTION_FIELDS].filter(
    (field) => !URL_ASSERTION_FIELDS.has(field) && field !== "frame",
  ),
);

/** Actions that need a value — either written down or referenced. */
const NEEDS_A_VALUE = new Set(["fill", "select", "press"]);

/**
 * The value half of a step: written down (`value`) or referenced (`valueFrom`), never
 * both and never neither where the action needs one.
 *
 * Three separate refusals, because they are three different mistakes:
 *
 *  - BOTH is a contradiction, and silently preferring one would make a spec that reads
 *    one way and replays the other.
 *  - AN UNSUPPORTED SCHEME (`vault.x`, `file:...`) is a reference nothing resolves; the
 *    message names the one form that works rather than only saying no.
 *  - A LITERAL `<secret:...>` is the redaction placeholder a recording writes when it
 *    withholds a classified value. It is not a value: replaying it types the
 *    placeholder into the field and reports the failure as a broken locator, which
 *    sends the reader looking at the selector instead of at the missing credential.
 */
function checkStepValue(step: SpecStep, at: string, problems: string[]): void {
  // PRESENCE, not type. Checking `typeof value === "string"` let two shapes through:
  // `value: null` and `value: 1234` beside a `valueFrom` both read as "no literal" here
  // while the audit's inputs field, which prefers whichever spelling is not undefined,
  // would report the literal for a step replay resolved from the environment. A spec
  // that says two things is refused for saying them, never silently reconciled.
  //
  // `undefined` is ABSENT, and only `undefined`: YAML cannot express it, so it only
  // ever arrives from a programmatic producer spreading a field it does not have — the
  // idiom every one of them uses for "no value here". `null` is a value the author
  // wrote down, which is why it is not the same answer.
  const carriesValue = step?.value !== undefined;
  const hasValue = typeof step?.value === "string";
  const hasReference = step?.valueFrom !== undefined;
  if (carriesValue && hasReference) {
    problems.push(
      `${at}: carries both value and valueFrom — a step's value is either written down or referenced, never both`,
    );
  }
  if (NEEDS_A_VALUE.has(step?.action) && !hasValue && !hasReference) {
    problems.push(
      `${at}: value (or valueFrom) is required for action "${step.action}"`,
    );
  }
  if (
    hasReference &&
    (typeof step.valueFrom !== "string" ||
      !VALUE_FROM_PATTERN.test(step.valueFrom))
  ) {
    problems.push(
      `${at}: valueFrom must match ${VALUE_FROM_FORM} — the only supported source is an environment variable, got "${String(step.valueFrom)}"`,
    );
  }
  // A non-string literal is neither a value nor a reference: named on its own, so the
  // reader is not left with only the exclusivity complaint above.
  if (carriesValue && !hasValue) {
    problems.push(
      `${at}: value must be a string, got ${JSON.stringify(step.value)}`,
    );
  }
  if (hasValue && SECRET_PLACEHOLDER_PATTERN.test(step.value as string)) {
    problems.push(
      `${at}: value "${step.value}" is a redaction placeholder, not a value — name where the real one comes from with valueFrom: ${VALUE_FROM_FORM}`,
    );
  }
}

/**
 * A frame chain, checked wherever one appears — a step's, an assertion's, a proposal's.
 *
 * `path` names the field for the reader ("step 3: frame", "step.frame"). Every refusal
 * names the LINK by its position, because a chain is read outermost-first and "the
 * second link" is the only way to say which `<iframe>` a reader should go and look at.
 */
export function checkFrameChain(
  value: unknown,
  path: string,
  problems: string[],
): void {
  if (!Array.isArray(value)) {
    problems.push(
      `${path} must be a list of frame references, outermost frame first`,
    );
    return;
  }
  // An empty chain is not "the top-level page" — that is what ABSENT means. Written
  // down, it is an author who meant to name a frame and named none, and silently
  // reading it as the page would run the step somewhere they did not ask for.
  if (value.length === 0) {
    problems.push(
      `${path} is empty — omit it entirely to address the top-level page`,
    );
    return;
  }
  value.forEach((link: unknown, position: number) => {
    const at = `${path}[${position}]`;
    if (typeof link !== "object" || link === null || Array.isArray(link)) {
      problems.push(`${at} must be a mapping naming one frame`);
      return;
    }
    const reference = link as Record<string, unknown>;
    for (const key of Object.keys(reference)) {
      if (!(FRAME_REF_FIELDS as readonly string[]).includes(key)) {
        problems.push(
          `${at}.${key} is not a frame field (one of ${FRAME_REF_FIELDS.join(" | ")})`,
        );
      }
    }
    const present = FRAME_REF_FIELDS.filter(
      (field) => reference[field] !== undefined,
    );
    // EXACTLY one, for the reason the url predicates are exactly one: these are three
    // alternative answers to "which frame", not independent facts to conjoin.
    if (present.length === 0) {
      problems.push(
        `${at} names no frame — give it one of ${FRAME_REF_FIELDS.join(", ")}`,
      );
      return;
    }
    if (present.length > 1) {
      problems.push(
        `${at} may name a frame only one way — got ${present.join(", ")}`,
      );
    }
    for (const field of present) {
      const named = reference[field];
      if (typeof named !== "string" || named.trim() === "") {
        problems.push(`${at}.${field} must be a non-empty string`);
      }
    }
  });
}

/**
 * The whole assertion grammar, in one place, so a HEALER's proposed assertion is held to
 * exactly the contract a captured or authored one is.
 *
 * It used to exist twice — here and, thinner, in heal/proposal.mts — and the thin copy
 * accepted what this one refuses: a non-canonical `url`, an uncompilable `urlPattern`, a
 * blank `testId`. A proposal carrying one of those was accepted at proposal time and
 * only refused later by `validateSpec`, from inside `applyProposal`, where the reader
 * sees a spec error rather than the bad proposal that caused it.
 *
 * `path` prefixes every field ("step 1: assert", "to"); `self` is how the assertion
 * refers to ITSELF inside a message ("assert", "to").
 */
export function checkAssertionFields(
  value: unknown,
  path: string,
  self: string,
  problems: string[],
): void {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    problems.push(`${path} must be a mapping of assertion fields`);
    return;
  }
  const assertion = value as Record<string, unknown>;
  const urlFieldsPresent = [...URL_ASSERTION_FIELDS].filter(
    (field) => assertion[field] !== undefined,
  );
  const hasUrlField = urlFieldsPresent.length > 0;
  const hasElementField = [...ELEMENT_ASSERTION_FIELDS].some(
    (field) => assertion[field] !== undefined,
  );
  // An assert is EITHER a page-level url check OR an element check, never both:
  // `hasText`/`containsText`/`visible` (and any locator) need an element to bind to,
  // and a url field satisfying the "needs a locator" rule below silently let that
  // requirement slide — the assertion loaded, replayed, and every element predicate
  // beside the url one went unchecked without a word about it.
  if (hasUrlField && hasElementField) {
    problems.push(
      `${path} cannot combine a url/urlPrefix/urlPattern predicate with an element assertion field — a url assertion is page-level and must stand alone`,
    );
  }
  // Exactly one, not "one or more": url/urlPrefix/urlPattern are three different
  // matching modes for the SAME check, not independent facts to conjoin — an assertion
  // that needs more than one mode is two assertions, one per step.
  if (urlFieldsPresent.length > 1) {
    problems.push(
      `${path} may carry only one of url, urlPrefix, urlPattern — got ${urlFieldsPresent.join(", ")}`,
    );
  }
  if (
    !assertion.testId &&
    !assertion.selector &&
    !assertion.role &&
    !assertion.text &&
    !assertion.url &&
    !assertion.urlPrefix &&
    !assertion.urlPattern
  ) {
    problems.push(`${path} needs a testId, selector, role, text, or url`);
  }
  // There is ONE address bar. A url assertion is a fact about the page, so scoping it
  // to a frame either means nothing (and reads as if it meant something) or silently
  // asserts the frame's own document URL — a different check under the same name.
  if (assertion.frame !== undefined) {
    if (hasUrlField) {
      problems.push(
        `${path} cannot carry a frame beside url/urlPrefix/urlPattern — a url assertion is page-level, and there is one page URL however many frames are open`,
      );
    }
    checkFrameChain(assertion.frame, `${path}.frame`, problems);
  }
  // Field TYPES, not just presence: YAML `visible: "false"` is a string, and an
  // unvalidated string is truthy — the runner would assert the opposite predicate.
  for (const field of ["hasText", "containsText"]) {
    if (assertion[field] !== undefined && typeof assertion[field] !== "string")
      problems.push(`${path}.${field} must be a string`);
  }
  // Locator fields must also be non-blank: `testId: "   "` targeted whitespace, and
  // `testId: ""` beside a valid selector won precedence and ignored it. A URL is never
  // legitimately empty either, so `url`/`urlPrefix` join the same check.
  for (const field of [
    "testId",
    "selector",
    "role",
    "name",
    "text",
    "url",
    "urlPrefix",
  ]) {
    const locator = assertion[field];
    if (
      locator !== undefined &&
      (typeof locator !== "string" || locator.trim() === "")
    )
      problems.push(`${path}.${field} must be a non-empty string`);
  }
  checkUrlShape(assertion, path, problems);
  if (assertion.visible !== undefined && typeof assertion.visible !== "boolean")
    problems.push(`${path}.visible must be a boolean`);
  if (assertion.exact !== undefined && typeof assertion.exact !== "boolean")
    problems.push(`${path}.exact must be a boolean`);
  // `name` filters `role`'s accessible name — alone it has nothing to filter.
  if (assertion.name !== undefined && assertion.role === undefined) {
    problems.push(`${path}.name requires ${self}.role`);
  }
  // An unknown assertion key is the quietest way to weaken a test: `containsTex`
  // loads, replays, and asserts nothing, so the step reads as covered while the
  // predicate it was written for never runs. The grammar is closed, and it is one
  // contract — a captured spec obeys it exactly as an authored one does, and so does a
  // healer's proposal.
  for (const field of Object.keys(assertion)) {
    if (!ASSERTION_FIELDS.has(field)) {
      problems.push(
        `${path}.${field} is not an assertion field (one of ${[...ASSERTION_FIELDS].join(" | ")})`,
      );
    }
  }
}

/**
 * `url`/`urlPrefix` must be absolute AND canonical; `urlPattern` must compile.
 *
 * Absolute (http/https): a relative value fed to Playwright's `toHaveURL` resolves
 * against `new URL(value, baseURL)`, and with no baseURL configured that either throws
 * or, if one is ever added later, silently starts comparing something other than what
 * Cypress/Puppeteer compare raw.
 *
 * Canonical (value === new URL(value).href): `toHaveURL(string)` normalizes its argument
 * through `new URL()` before comparing, so a merely-absolute-but-not-canonical value (an
 * uppercase scheme, an unresolved "/a/../") could still PASS in replay/Playwright while
 * FAILING Cypress's and Puppeteer's raw string compare. `urlPrefix` is checked
 * identically, as a complete URL string in its own right: a bare origin canonicalizes to
 * itself WITH a trailing "/" (`new URL("http://h:1").href` is `"http://h:1/"`).
 */
function checkUrlShape(
  assertion: Record<string, unknown>,
  path: string,
  problems: string[],
): void {
  for (const field of ["url", "urlPrefix"]) {
    const value = assertion[field];
    if (typeof value !== "string" || value.trim() === "") continue;
    if (!/^https?:\/\//i.test(value)) {
      problems.push(`${path}.${field} must be an absolute http(s) URL`);
      continue;
    }
    let canonical: string;
    try {
      canonical = new URL(value).href;
    } catch {
      problems.push(
        `${path}.${field} must be a canonical URL — "${value}" does not parse as one at all`,
      );
      continue;
    }
    if (value !== canonical) {
      problems.push(
        `${path}.${field} must be a canonical URL (value === new URL(value).href) — got "${value}", the canonical form is "${canonical}"`,
      );
    }
  }
  // `urlPattern` must additionally compile — a malformed regex must fail loudly at load,
  // never at the moment replay tries to test a URL against it.
  if (assertion.urlPattern === undefined) return;
  if (
    typeof assertion.urlPattern !== "string" ||
    assertion.urlPattern.trim() === ""
  ) {
    problems.push(`${path}.urlPattern must be a non-empty string`);
    return;
  }
  try {
    new RegExp(assertion.urlPattern);
  } catch {
    problems.push(`${path}.urlPattern must be a valid regular expression`);
  }
}

export function validateSpec(candidate: unknown): asserts candidate is Spec {
  const problems: string[] = [];
  const spec = candidate as Partial<Spec>;

  if (typeof spec?.name !== "string" || spec.name.trim() === "")
    problems.push("name must be a non-empty string");
  if (typeof spec?.startUrl !== "string" || spec.startUrl.trim() === "")
    problems.push("startUrl must be a non-empty string");
  if (!Array.isArray(spec?.steps) || spec.steps.length === 0) {
    problems.push("steps must be a non-empty array");
    throw new SpecValidationError(problems);
  }

  const seenIds = new Set<string>();
  spec.steps.forEach((step: SpecStep, position: number) => {
    const at = `step ${position + 1}`;
    // Identity: required, unique, and never positional. Evidence correlation depends on
    // it surviving a repair that renumbers everything after an insertion.
    if (typeof step?.id !== "string" || step.id.trim() === "") {
      problems.push(`${at}: id is required and must be a non-empty string`);
    } else if (seenIds.has(step.id)) {
      problems.push(
        `${at}: duplicate id "${step.id}" — evidence would be ambiguous`,
      );
    } else {
      seenIds.add(step.id);
    }
    // index is display ordering, so it MUST track position — that is what makes it safe
    // to renumber and unsafe to correlate on.
    if (step?.index !== position + 1)
      problems.push(`${at}: index must be ${position + 1}, got ${step?.index}`);
    if (!ACTION_KINDS.includes(step?.action)) {
      problems.push(
        `${at}: action must be one of ${ACTION_KINDS.join(" | ")}, got ${String(step?.action)}`,
      );
    }
    // Every action needs a target — waitFor included: it is the locator to wait for,
    // and without one the step is malformed input, not a replay failure (review P2).
    if (typeof step?.target !== "string" || step.target.trim() === "") {
      problems.push(`${at}: target is required for action "${step?.action}"`);
    }
    // press carries the key to send, so it needs a value exactly as fill/select do —
    // without one the replay runner cannot know what to press. A `valueFrom` reference
    // satisfies the same requirement: the value exists, it is just not written down
    // here (types.mts, SpecStep.valueFrom).
    checkStepValue(step, at, problems);
    // The rule the breakage corpus exists to enforce: state changes need an assertion or
    // a drift that every locator survives will pass silently.
    if (STATE_CHANGING.has(step?.action) && step?.assert === undefined) {
      problems.push(
        `${at}: action "${step.action}" changes state and must carry an assert (state-change rule)`,
      );
    }
    // A frame chain is part of a step's ADDRESS: it says where `target` is resolved.
    // Checked here rather than only at replay so a chain that names nothing is a load
    // error, exactly as an unknown assertion key is.
    if (step?.frame !== undefined) {
      // A `goto` navigates the PAGE — there is no frame-scoped navigation in this
      // grammar. A chain beside one would be silently ignored by replay while reading
      // as though the navigation happened inside the frame.
      if (step.action === "goto") {
        problems.push(
          `${at}: a goto navigates the page, so it cannot carry a frame`,
        );
      }
      checkFrameChain(step.frame, `${at}: frame`, problems);
    }
    // `assert: null` (or false, 0, "", []) is neither absent nor a mapping: it passed
    // the presence check above and dodged every field check by being falsy, then the
    // runner dereferenced it after opening a browser. Malformed at load — and named by
    // the shared checker, which is the same one a healer's proposal goes through.
    if (step?.assert !== undefined) {
      checkAssertionFields(step.assert, `${at}: assert`, "assert", problems);
    }
  });

  if (problems.length > 0) throw new SpecValidationError(problems);
}

export function loadSpec(yamlText: string): Spec {
  const parsed: unknown = parseYaml(yamlText);
  validateSpec(parsed);
  return parsed;
}

/** Serialise for commit. `yaml` preserves insertion order by default, which is what
 *  keeps a repair diff to the single line that actually changed. */
export function saveSpec(spec: Spec): string {
  return stringifyYaml(spec, null, { lineWidth: 0 });
}

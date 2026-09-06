/**
 * The spec format — the product surface.
 *
 * This is what a human reads in a repair PR and what the heal loop machine-edits, so it
 * is optimised for exactly one thing: a repair must be a SMALL, OBVIOUS diff. That is
 * why it is structured data and not code (design decision, ratified 2026-09-01) —
 * an agent editing YAML changes one field; an agent editing TypeScript can change
 * anything.
 *
 * Two rules the format enforces structurally rather than by convention:
 *
 *  1. `assert` is not optional on a step that changes state. The breakage corpus measured a drift
 *     class (data drift) that every locator survives and only an assertion catches.
 *  2. A repair may change `target`; changing `expected` is a HUMAN decision. The heal
 *     loop writes proposals into `proposedAssertChange`, never into `assert`.
 *     When an order belongs to the wrong customer, every locator can still resolve;
 *     rewriting the expected customer would turn a caught wrong-order bug into a green test.
 *
 * CONTRACT STATUS: RATIFIED 2026-09-01 (on review) — no longer a draft
 * proposal. The heal loop builds on this shape. Two semantics the review pinned down, encoded
 * by the replay runner (replay/assertions.mts, replay/runner.mts) and tested there:
 *
 *  - An `Assertion`'s fields are a CONJUNCTION: every present field must hold. A LOCATOR
 *    is resolved by precedence when more than one is given: `testId` > `selector` >
 *    `role` > `text` (the same order the brief tells a healer to prefer for a step's
 *    `target`). `role` and `text` follow Playwright's own matching: the accessible
 *    `name` (paired with `role`) and `text` are substring matches by default; `exact:
 *    true` asks for a whole-string match. `hasText` is exact, `containsText` is a
 *    substring. VISIBILITY IS IMPLIED unless `visible: false` is explicit — an
 *    assertion describes what the reviewer sees in the replay, and text matchers alone
 *    pass on hidden DOM (measured on breakage class 3, changed flow: a `hasText` on the hidden
 *    confirmation heading passed while an interstitial was on screen). So a locator
 *    with no predicate is never vacuous, and class 4 cannot leak through.
 *  - `startUrl` is provenance, not an implicit navigation. The runner never navigates
 *    to it; the captured `goto` step does, so step 1's evidence window stays exact.
 *  - `url`/`urlPrefix`/`urlPattern` assert the current page URL rather than an element,
 *    so it satisfies the "needs a locator" rule on its own — a navigation proposal no
 *    longer has to assert on the destination heading as a proxy. EXACTLY ONE of the
 *    three may be present: they are different matching modes for the same check, not
 *    independent facts to conjoin, and never alongside an element field either.
 */

export type ActionKind =
  "goto" | "click" | "fill" | "press" | "select" | "waitFor";

/**
 * ONE link of a frame chain: how to find a single nested browsing context inside the
 * one that contains it.
 *
 * Exactly one key. The three ways a frame can be addressed are not facts to conjoin —
 * they are alternative answers to the same question, and a link carrying two of them
 * would let a spec read one way and resolve the other.
 *
 *  - `selector`  the OWNING `<iframe>` element, addressed in the PARENT's document. The
 *                strongest form: the element is proven where it lives, exactly as a
 *                step's own target is.
 *  - `name`      the frame's `name` attribute — the fallback when the owning element is
 *                not reachable from the parent (a cross-origin child, whose element a
 *                recording cannot prove).
 *  - `url`       the frame's own document URL, matched whole.
 *  - `urlPrefix` the same, matched as a prefix, so a fragment or a query the frame gains
 *                at run time still names the same frame. The last resort a recording
 *                falls back to: a URL is provenance, not identity.
 *
 * The two URL forms are spelled the way an `Assertion` spells them, for the same reason
 * — one vocabulary for "this exact string" and "this string starts it", everywhere.
 */
export interface FrameRef {
  selector?: string;
  name?: string;
  url?: string;
  urlPrefix?: string;
}

/** Outermost first: `[a, b]` means "the frame `b` inside the frame `a` inside the page". */
export type FrameChain = FrameRef[];

/** The keys a `FrameRef` may carry, as a closed set — a typo is a load error, never a
 *  link that silently addresses nothing. */
export const FRAME_REF_FIELDS: readonly (keyof FrameRef)[] = [
  "selector",
  "name",
  "url",
  "urlPrefix",
];

/** An assertion on observable state after a step. Deliberately small — each variant maps
 *  to one Playwright expectation, so a spec never smuggles in arbitrary logic. */
export interface Assertion {
  /** Prefer a testId; it survives styling churn. */
  testId?: string;
  /** Fallback CSS/text selector when no testId exists. */
  selector?: string;
  /** ARIA role — resolves via Playwright's `getByRole`. Pair with `name` to also match
   *  the accessible name; `role` alone matches any element with that role. */
  role?: string;
  /** Accessible name filter for `role`. Only meaningful alongside `role`. */
  name?: string;
  /** Visible text — resolves via Playwright's `getByText`. */
  text?: string;
  /** Whole-string match for `name` (with `role`) or `text`. Default false: a substring
   *  match, matching Playwright's own default for both locators. */
  exact?: boolean;
  hasText?: string;
  containsText?: string;
  visible?: boolean;
  /** Exact match against the current page URL. Satisfies the locator requirement on
   *  its own — a navigation is a page-level fact, not an element one. */
  url?: string;
  /** The current page URL must start with this string. */
  urlPrefix?: string;
  /** The current page URL must match this regular expression (source text, no
   *  delimiters). Validated as a compilable pattern at load time. */
  urlPattern?: string;
  /**
   * The nested browsing context this assertion's ELEMENT lives in, outermost first.
   *
   * Absent means the step's own frame — an assertion describes the consequence of the
   * action it rides on, and those are almost always the same document. It is spelled out
   * only when they differ: an action inside a payment iframe whose confirmation renders
   * on the host page.
   *
   * REFUSED on a `url`/`urlPrefix`/`urlPattern` assertion. There is one address bar; a
   * url assertion is a fact about the PAGE, and scoping it to a frame would either be
   * ignored (and read as if it were honoured) or assert the frame's own document URL,
   * which is not what "the current page URL" means anywhere else in this grammar.
   */
  frame?: FrameChain;
}

export interface SpecStep {
  /**
   * Stable identity, assigned once at capture and NEVER reassigned.
   *
   * Evidence is addressed by this, not by position. A flow repair inserts a step, which
   * renumbers every `index` after it — so if evidence keyed on position, a segment
   * captured before the repair would silently start pointing at a different step. That
   * is the failure this field exists to prevent, and it bites hardest on exactly the
   * repair class (flow change) that most differentiates us.
   *
   * A repair rewrites `target`; it must carry `id` through untouched. An inserted step
   * gets a fresh one.
   */
  id: string;
  /**
   * 1-based position for display and diff readability ONLY. Always equals the step's
   * position in the array, so it renumbers freely on insertion. Never address evidence
   * with this — use `id`.
   */
  index: number;
  action: ActionKind;
  /** Locator or URL. THIS is the field a heal may rewrite. */
  target?: string;
  /** Value for fill/press/select. Exactly one of `value`/`valueFrom` is present. */
  value?: string;
  /**
   * Where the value comes from, for a value that must not be IN the spec.
   *
   * The spec is committed, read in a pull request and attached to evidence; a
   * credential typed during capture would travel all three ways. So the spec names the
   * ENVIRONMENT VARIABLE instead (`env.APPROVE_AN_ORDER_PASSWORD`) and replay resolves
   * it at step time — the same rule `apiKeyFrom` already states for a healer profile,
   * and the only form that lets a recorded sign-in be reviewed AND replayed.
   *
   * Exclusive with `value`: a step carries one or the other, never both and never
   * neither where the action needs a value.
   */
  valueFrom?: string;
  /**
   * The nested browsing context this step's `target` is addressed in, outermost first.
   *
   * Absent means the top-level page, which is what every step recorded before this field
   * existed means — so a spec without it reads and replays exactly as it did.
   *
   * A frame chain is part of a step's ADDRESS, not of its expectation: it says where the
   * locator is resolved, the same way `target` says what is resolved there. That is why
   * a repair may rewrite it, and only as part of rewriting the target.
   */
  frame?: FrameChain;
  /** What must be true after this step. */
  assert?: Assertion;
  /** Set by the heal loop when it believes the ASSERTION (not the locator) is stale.
   *  Never applied automatically — it renders in the PR for a human to accept. */
  proposedAssertChange?: {
    from: Assertion;
    to: Assertion;
    reason: string;
  };
}

export interface Spec {
  name: string;
  /** Where the capture started. Provenance only — replay does NOT navigate here; the
   *  first `goto` step does. See the contract note in the file header. */
  startUrl: string;
  /** Provenance: which driver captured this, and when. */
  capturedBy?: string;
  capturedAt?: string;
  steps: SpecStep[];
}

export const ACTION_KINDS: readonly ActionKind[] = [
  "goto",
  "click",
  "fill",
  "press",
  "select",
  "waitFor",
];

/**
 * The one supported `valueFrom` scheme, and the shape of the name it may carry.
 *
 * `env.` is deliberately the only scheme: every other place a secret could come from
 * (a vault, a file, a keychain) is a dependency and a network call this format does not
 * want, and every CI system already knows how to put a variable in the environment. A
 * name is SHOUT_CASE because that is what an environment variable is — accepting
 * `env.password` would mean a spec that reads correctly and resolves nothing.
 */
export const VALUE_FROM_PATTERN = /^env\.[A-Z][A-Z0-9_]*$/;

/** The scheme spelled out for an error message, so a refusal says what would work. */
export const VALUE_FROM_FORM = "env.<NAME>";

/**
 * What a redacted value looks like in a spec (`capture/sensitive-fields.mts` writes the
 * form). It is NEVER a value: a step still carrying one is a recording nobody finished,
 * and replaying it types the placeholder into the field and reports the resulting
 * failure as if the locator were broken.
 */
export const SECRET_PLACEHOLDER_PATTERN = /^<secret:[a-z-]+>$/;

/** The environment variable a `valueFrom` names. Callers validate the form first. */
export function envVariableOf(valueFrom: string): string {
  return valueFrom.slice("env.".length);
}

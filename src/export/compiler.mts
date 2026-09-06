/**
 * The walk: spec in, source out.
 *
 * The spec is the source of truth and the export is a build artifact — so this file
 * knows the ORDER of things (header, preamble, step by step, postamble) and nothing
 * about any framework. Every framework-specific decision is a table lookup in
 * `locators.mts` or in the target's own `actions`/`assertions` tables, and a lookup
 * that misses is a refusal, never a silent omission: an exported test that quietly
 * dropped an assertion still goes green in the consumer's CI, which is the exact
 * failure this repo exists to refuse.
 */
import {
  FRAME_REF_FIELDS,
  VALUE_FROM_FORM,
  VALUE_FROM_PATTERN,
  envVariableOf,
  type ActionKind,
  type Assertion,
  type FrameChain,
  type Spec,
  type SpecStep,
} from "../spec/types.mts";
import {
  anyFrameLookup,
  assertionKindsOf,
  controlCharacterIn,
  isUnsupported,
  locatorFormOf,
  urlAssertionKindsOf,
  LOCATOR_TABLE,
  type AssertionKind,
  type Emitted,
  type TargetName,
  type Unsupported,
  type UrlAssertionKind,
} from "./locators.mts";

/** Raised instead of emitting code that would not mean what the spec means. */
export class UnsupportedConstructError extends Error {
  constructor(
    readonly target: TargetName,
    readonly construct: string,
    readonly reason: string,
  ) {
    super(
      `cannot export to ${target}: ${construct} is unsupported — ${reason}`,
    );
    this.name = "UnsupportedConstructError";
  }
}

/** Which text/URL predicates a spec uses — a target emits only the helpers it needs. */
export interface SpecUsage {
  readonly hasText: boolean;
  readonly containsText: boolean;
  readonly url: boolean;
  readonly urlPrefix: boolean;
  readonly urlPattern: boolean;
  /** Whether any step reads its value from the environment — the targets emit the
   *  missing-variable helper, and the header its list, only when one does. */
  readonly valueFrom: boolean;
  /** Whether any step or assertion is scoped to a frame at all. */
  readonly frames: boolean;
  /**
   * Whether any frame link names its frame by `name`/`url`/`urlPrefix` rather than by
   * the owning `<iframe>` selector.
   *
   * The distinction is Playwright's: `frameLocator()` takes a selector and nothing else,
   * so a selector-only chain compiles to the idiom a reader expects, and only a chain
   * that asks a parent for a NAMED child needs the emitted lookup helper.
   */
  readonly frameLookup: boolean;
}

/** A frame-scoped step, as target code: the statements that resolve the chain, and the
 *  expression the step's locators are built on. */
export interface FrameScope {
  readonly setup: string[];
  readonly expression: string;
}

export interface Target {
  readonly name: TargetName;
  /** Appended to the spec name to suggest a file name. */
  readonly extension: string;
  /** Indentation for the step body. */
  readonly indent: string;
  preamble(spec: Spec, usage: SpecUsage): string[];
  postamble(spec: Spec): string[];
  /** step kind -> statement(s). */
  readonly actions: Partial<
    Record<ActionKind, (step: SpecStep, scope: string) => Emitted>
  >;
  /** How this target names the top-level page — the scope of every step that carries no
   *  frame chain, and the root the frame helper walks down from. */
  readonly pageExpression: string;
  /** assertion predicate -> expectation(s), given this target's locator code. */
  readonly assertions: Partial<
    Record<
      AssertionKind,
      (locator: string, assertion: Assertion, scope: string) => Emitted
    >
  >;
  /** URL predicate -> expectation(s). No locator: these are page-level, not element. */
  readonly urlAssertions: Partial<
    Record<UrlAssertionKind, (assertion: Assertion) => Emitted>
  >;
  /**
   * How this target addresses a nested browsing context — ABSENT when it cannot.
   *
   * Absent is a real answer, not an oversight: a target with no first-party frame
   * command would have to be handed an approximation, and an exported test that means
   * something subtly other than the spec fails silently in the consumer's own CI. That
   * is the failure this whole file refuses, so a spec with a frame chain is refused for
   * such a target by name, exactly as a `role` locator is.
   */
  readonly frames?: {
    /** Emitted once, in the preamble, when the spec uses frames. */
    helper(usage: SpecUsage): string[];
    /** `name` is unique within the generated file, so nested scopes never collide. */
    scope(chain: FrameChain, name: string): FrameScope | Unsupported;
  };
}

function demand(
  emitted: Emitted,
  target: TargetName,
  construct: string,
): string {
  if (isUnsupported(emitted)) {
    throw new UnsupportedConstructError(target, construct, emitted.unsupported);
  }
  return emitted;
}

function usageOf(spec: Spec): SpecUsage {
  return {
    hasText: spec.steps.some((step) => step.assert?.hasText !== undefined),
    containsText: spec.steps.some(
      (step) => step.assert?.containsText !== undefined,
    ),
    url: spec.steps.some((step) => step.assert?.url !== undefined),
    urlPrefix: spec.steps.some((step) => step.assert?.urlPrefix !== undefined),
    urlPattern: spec.steps.some(
      (step) => step.assert?.urlPattern !== undefined,
    ),
    valueFrom: spec.steps.some((step) => step.valueFrom !== undefined),
    frames: frameChainsOf(spec).length > 0,
    frameLookup: frameChainsOf(spec).some(anyFrameLookup),
  };
}

/** Every frame chain the spec carries — a step's and an assertion's alike. */
function frameChainsOf(spec: Spec): FrameChain[] {
  const chains: FrameChain[] = [];
  for (const step of spec.steps) {
    if (step.frame !== undefined) chains.push(step.frame);
    if (step.assert?.frame !== undefined) chains.push(step.assert.frame);
  }
  return chains;
}

/** Every environment variable the generated file needs, in step order, once each. */
export function requiredVariables(spec: Spec): string[] {
  const names: string[] = [];
  for (const step of spec.steps) {
    if (step.valueFrom === undefined) continue;
    const name = envVariableOf(step.valueFrom);
    if (!names.includes(name)) names.push(name);
  }
  return names;
}

/**
 * The code for a step's value: the literal it carries, or the environment read its
 * reference names.
 *
 * One function for all three targets, because the CHOICE is the spec's and only the
 * spelling of the read is the target's. Every target pairs the read with a call that
 * throws by name, so an unset variable fails loudly at the step instead of typing
 * "undefined" into a field and failing three assertions later.
 */
export function valueCode(
  step: SpecStep,
  envRead: (variable: string) => string,
  literal: (value: string) => string,
): string {
  if (step.valueFrom === undefined) return literal(step.value as string);
  return envRead(envVariableOf(step.valueFrom));
}

/** JavaScript line terminators that JSON.stringify leaves bare. */
const JS_LINE_SEPARATORS = /[\u2028\u2029]/g;

/**
 * Spec-authored text as a single-line comment payload.
 *
 * Step ids and spec paths are authored by humans and carried verbatim by the parser,
 * which accepts line terminators inside them. Interpolated raw, one of those ends the
 * `//` comment and everything after it becomes executable code in the consumer's repo.
 * `JSON.stringify` escapes losslessly, and U+2028/U+2029 — line terminators in
 * JavaScript that JSON leaves bare — are escaped after it for the same reason.
 */
export function commentText(value: string): string {
  return JSON.stringify(value).replace(
    JS_LINE_SEPARATORS,
    (character) => `\\u${(character.codePointAt(0) ?? 0).toString(16)}`,
  );
}

/**
 * What the RUNNER this file is for may still capture, and how to turn that off.
 *
 * The export leaves this repository: nothing here scrubs the consumer's artifacts, and
 * a guarantee that only holds inside this harness has to be stated where it stops
 * holding. Playwright's trace and Puppeteer's own instrumentation record what a step
 * typed; Cypress's command log is suppressed per-command by the emitter itself, but its
 * screenshots and video still show the page.
 */
function tracingWarning(target: TargetName): string[] {
  const lines: Record<TargetName, string[]> = {
    playwright: [
      "// A Playwright trace records the arguments of every action, so a trace taken on",
      "// these steps holds the resolved values. Run with `trace: 'off'` (or discard the",
      "// trace) for a suite that fills secrets, and treat any kept trace as a secret.",
    ],
    puppeteer: [
      "// Nothing here writes the resolved values down, but a screenshot, a video or a",
      "// devtools capture taken around these steps shows the page they were typed into.",
      "// Treat any such artifact from this script as a secret.",
    ],
    cypress: [
      "// The referenced steps below pass `{ log: false }`, so the resolved values stay",
      "// out of the Cypress command log — and therefore out of the screenshots and the",
      "// video rendered from it. The page itself is still shown; treat a run's video as",
      "// a secret if the field displays what was typed.",
    ],
  };
  return lines[target];
}

/** The provenance header every generated file carries, plus — when the spec references
 *  any — the variables the environment has to supply before this file can run, and what
 *  the runner itself may still capture. Named in the file itself because that is what
 *  the person running it reads, not the spec. */
export function headerLines(
  source: string,
  generatedOn: string,
  variables: string[] = [],
  target: TargetName = "playwright",
): string[] {
  return [
    `// Generated by Formic from ${commentText(source)} on ${commentText(generatedOn)}.`,
    "// The spec is the source of truth: regenerate this file, do not hand-edit it.",
    ...(variables.length === 0
      ? []
      : [
          "//",
          "// Requires these environment variables (the spec references them rather",
          "// than carrying their values):",
          ...variables.map((name) => `//   ${name}`),
          "//",
          ...tracingWarning(target),
        ]),
  ];
}

/** Whether this assertion carries any element-shaped field — a locator or one of the
 *  predicates that only means something applied to one (`hasText`/`containsText`/
 *  `visible`). The validator refuses this alongside a `url`/`urlPrefix`/`urlPattern`
 *  field, but `compile` can be called directly on a hand-built `Spec` that skipped
 *  validation — gating on the full field set, not just the four locator fields, means
 *  that bypass still demands a locator (and gets the usual refusal if none is present)
 *  instead of silently omitting the element predicates from the generated file. */
function hasElementField(assertion: Assertion): boolean {
  return (
    assertion.testId !== undefined ||
    assertion.selector !== undefined ||
    assertion.role !== undefined ||
    assertion.name !== undefined ||
    assertion.text !== undefined ||
    assertion.exact !== undefined ||
    assertion.hasText !== undefined ||
    assertion.containsText !== undefined ||
    assertion.visible !== undefined
  );
}

/**
 * The code that resolves a frame chain for one step or assertion, or a refusal.
 *
 * A target with no `frames` entry is refused BY NAME here rather than silently compiling
 * the step against the top-level page — which is the same statement with a different
 * meaning, green in the consumer's CI and wrong.
 */
function frameScope(
  chain: FrameChain | undefined,
  target: Target,
  name: string,
): FrameScope {
  if (chain === undefined)
    return { setup: [], expression: target.pageExpression };
  if (target.frames === undefined) {
    throw new UnsupportedConstructError(
      target.name,
      "a frame chain",
      "this target has no first-party way to address a nested browsing context, and approximating one would export a test that means something the spec does not say",
    );
  }
  const scope = target.frames.scope(chain, name);
  if ("unsupported" in scope) {
    throw new UnsupportedConstructError(
      target.name,
      "a frame chain",
      scope.unsupported,
    );
  }
  return scope;
}

function assertionLines(
  assertion: Assertion,
  target: Target,
  stepScope: FrameScope,
  scopeName: string,
): string[] {
  const lines: string[] = [];
  for (const kind of urlAssertionKindsOf(assertion)) {
    const emit = target.urlAssertions[kind];
    if (emit === undefined) {
      throw new UnsupportedConstructError(
        target.name,
        `the assertion \`${kind}\``,
        "this target has no equivalent expectation",
      );
    }
    lines.push(
      ...demand(
        emit(assertion),
        target.name,
        `the assertion \`${kind}\``,
      ).split("\n"),
    );
  }

  if (!hasElementField(assertion)) {
    // A url-only assertion is a page-level fact and never needed an element locator.
    if (lines.length > 0) return lines;
    throw new UnsupportedConstructError(
      target.name,
      "an assertion",
      "it has neither testId, selector, role, text, nor url",
    );
  }
  const form = locatorFormOf(assertion);
  if (form === null) {
    throw new UnsupportedConstructError(
      target.name,
      "an assertion",
      "it has neither testId, selector, role, nor text",
    );
  }
  // An assertion inherits the step's frame unless it names one of its own — the same
  // rule replay applies, so the exported test and the harness agree about where an
  // expectation is checked.
  // Its OWN setup only. An inherited scope was already resolved by the step, and
  // re-emitting the setup would declare the same const twice in one test body.
  const own =
    assertion.frame === undefined
      ? undefined
      : frameScope(assertion.frame, target, scopeName);
  const scope = own ?? stepScope;
  lines.unshift(...(own?.setup ?? []));
  const locator = demand(
    LOCATOR_TABLE[target.name][form](assertion, scope.expression),
    target.name,
    `the assertion locator \`${form}\``,
  );
  for (const kind of assertionKindsOf(assertion)) {
    const emit = target.assertions[kind];
    if (emit === undefined) {
      throw new UnsupportedConstructError(
        target.name,
        `the assertion \`${kind}\``,
        "this target has no equivalent expectation",
      );
    }
    lines.push(
      ...demand(
        emit(locator, assertion, scope.expression),
        target.name,
        `the assertion \`${kind}\``,
      ).split("\n"),
    );
  }
  return lines;
}

function stepLines(step: SpecStep, target: Target): string[] {
  const emit = target.actions[step.action];
  if (emit === undefined) {
    throw new UnsupportedConstructError(
      target.name,
      `the step action \`${step.action}\``,
      "this target has no equivalent command",
    );
  }
  const scope = frameScope(step.frame, target, `frame${step.index}`);
  const lines = [
    `// step ${step.index} (${commentText(step.id)})`,
    ...scope.setup,
    ...demand(
      emit(step, scope.expression),
      target.name,
      `the step action \`${step.action}\``,
    ).split("\n"),
  ];
  if (step.assert !== undefined) {
    lines.push(
      ...assertionLines(step.assert, target, scope, `assertFrame${step.index}`),
    );
  }
  return lines;
}

/** Assertion fields whose text reaches a generated string literal or regex. */
const ASSERTION_TEXT_FIELDS = [
  "testId",
  "selector",
  "role",
  "name",
  "text",
  "hasText",
  "containsText",
] as const;

/**
 * Refuses spec-authored text that no target can carry, before any of them tries.
 *
 * Every value below lands in an emitted string literal or regex. Quotes, backslashes
 * and line separators all have lossless escapes and are escaped; control characters
 * below 0x20 do not — a CSS attribute value cannot hold one and a line break ends a
 * regex literal — so they are refused here, once, by name, for all three targets
 * rather than in each emitter.
 */
function demandNoControlCharacters(spec: Spec, target: TargetName): void {
  const refuse = (value: string | undefined, description: string): void => {
    if (value === undefined) return;
    const control = controlCharacterIn(value);
    if (control !== null) {
      throw new UnsupportedConstructError(
        target,
        description,
        `it contains the control character ${control}, which has no escape in generated source`,
      );
    }
  };
  refuse(spec.name, "the spec name");
  for (const step of spec.steps) {
    refuse(step.target, `the target of step ${step.index}`);
    refuse(step.value, `the value of step ${step.index}`);
    // A reference reaches generated source as a bare identifier, where nothing can
    // escape it. The validator already refuses any other shape; `compile` can be
    // called on a hand-built spec that skipped it, so it is refused here too.
    if (
      step.valueFrom !== undefined &&
      !VALUE_FROM_PATTERN.test(step.valueFrom)
    ) {
      throw new UnsupportedConstructError(
        target,
        `the valueFrom of step ${step.index}`,
        `it is not ${VALUE_FROM_FORM}, and no target can read a source it cannot name`,
      );
    }
    for (const field of ASSERTION_TEXT_FIELDS) {
      refuse(
        step.assert?.[field] as string | undefined,
        `the assertion \`${field}\` on step ${step.index}`,
      );
    }
    // A frame link's own text reaches a generated string literal exactly as a locator
    // does — as a selector, a name compared with ===, or a URL prefix.
    for (const [chain, where] of [
      [step.frame, "frame"],
      [step.assert?.frame, "assertion frame"],
    ] as const) {
      chain?.forEach((link, position) => {
        for (const field of FRAME_REF_FIELDS) {
          refuse(
            link[field],
            `the ${where} \`${field}\` of link ${position + 1} on step ${step.index}`,
          );
        }
      });
    }
  }
}

/** Walks the spec into a target's source text. Throws `UnsupportedConstructError`
 *  on the first construct the target cannot express. */
export function compile(
  spec: Spec,
  target: Target,
  source: string,
  generatedOn: string,
): string {
  demandNoControlCharacters(spec, target.name);
  const usage = usageOf(spec);
  const lines = [
    ...headerLines(source, generatedOn, requiredVariables(spec), target.name),
    ...target.preamble(spec, usage),
  ];
  spec.steps.forEach((step, position) => {
    if (position > 0) lines.push("");
    for (const line of stepLines(step, target)) {
      lines.push(line === "" ? "" : target.indent + line);
    }
  });
  lines.push(...target.postamble(spec));
  return `${lines.join("\n")}\n`;
}

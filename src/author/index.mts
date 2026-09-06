/**
 * Scripted authoring: the Recorder exposed as a browser-free path from the DSL
 * straight to a spec, and the deterministic way to extend one. Both compose the same two
 * moves — parse the DSL (`./dsl.mts`), assign identity — and both hand the result through
 * `spec/parse.mts`'s own `validateSpec`, so an authored or extended spec obeys every rule a
 * captured one does; there is no second, looser grammar hiding behind this door.
 *
 * Deterministic means BYTE-REPRODUCIBLE here, not merely offline: the same input authors
 * the same YAML every time, so an authored spec can be regenerated and diffed against the
 * copy in the repo. That costs the two sources of drift a capture is entitled to — step
 * ids come from position rather than randomness, and `capturedAt` honours
 * `SOURCE_DATE_EPOCH` (`./stamp.mts`).
 */
import { validateSpec } from "../spec/parse.mts";
import type { Spec, SpecStep } from "../spec/types.mts";
import { DslParseError, parseDsl, type DslStep } from "./dsl.mts";
import { authoredCapturedAt } from "./stamp.mts";

/** Omits `value`/`assert` entirely when the DSL step did not carry them, rather than
 *  setting them to `undefined` — keeps an authored spec object identical in shape to one
 *  that round-tripped through YAML (which drops undefined-valued keys on stringify), so
 *  authorSpec/extendSpec's own output needs no special-casing in a deepEqual comparison. */
/**
 * The id an authored step carries when the input does not name one: its 1-based position,
 * so the same input always produces the same bytes. Stable under extension too — an
 * extension only ever appends, so no existing step's position, and therefore no existing
 * step's derived id, can change.
 *
 * `taken` holds every id already spoken for — the explicit ids in this input and, for an
 * extension, the existing spec's. A step named `id: s2` would otherwise collide with the
 * default for position 2 and get a legal document refused as a duplicate, so a taken name
 * is stepped past with a numeric suffix. The walk is ordered and the suffix ascends, so
 * the outcome is still the same bytes for the same input.
 */
function derivedStepId(index: number, taken: ReadonlySet<string>): string {
  const base = `s${index}`;
  if (!taken.has(base)) return base;
  for (let suffix = 2; ; suffix++) {
    const candidate = `${base}-${suffix}`;
    if (!taken.has(candidate)) return candidate;
  }
}

/** Assigns ids over a run of DSL steps: explicit ids win, derived ids fill the rest and
 *  step past anything already taken. Each assignment joins `taken`, so two derived ids can
 *  never land on the same name either. */
function assignIds(
  steps: readonly DslStep[],
  firstIndex: number,
  reserved: readonly string[],
): string[] {
  const taken = new Set<string>(reserved);
  for (const step of steps) if (step.id !== undefined) taken.add(step.id);
  return steps.map((step, position) => {
    if (step.id !== undefined) return step.id;
    const id = derivedStepId(firstIndex + position, taken);
    taken.add(id);
    return id;
  });
}

function toSpecStep(step: DslStep, index: number, id: string): SpecStep {
  const specStep: SpecStep = {
    id,
    index,
    action: step.action,
    target: step.target,
  };
  if (step.value !== undefined) specStep.value = step.value;
  if (step.assert !== undefined) specStep.assert = step.assert;
  return specStep;
}

/** DSL text → a fresh, fully-assigned Spec. `name` and `startUrl` are required at the top
 *  of the document — a spec authored from scratch has no earlier capture to inherit them
 *  from. */
export function authorSpec(input: string): Spec {
  const doc = parseDsl(input);
  if (!doc.name) throw new DslParseError(["name is required"]);
  if (!doc.startUrl) throw new DslParseError(["startUrl is required"]);

  const ids = assignIds(doc.steps, 1, []);
  const steps = doc.steps.map((step, position) =>
    toSpecStep(step, position + 1, ids[position]),
  );
  const spec: Spec = {
    name: doc.name,
    startUrl: doc.startUrl,
    capturedBy: "author",
    capturedAt: authoredCapturedAt(),
    steps,
  };
  validateSpec(spec);
  return spec;
}

/**
 * Appends DSL-authored steps to an existing spec, deterministically. Every existing step
 * keeps its `id` AND its `index` untouched — the additions are appended, never spliced in,
 * so nothing before them ever renumbers. `name`/`startUrl` in `additions` are ignored: an
 * extension grows one spec, it does not rename it.
 */
export function extendSpec(existing: Spec, additions: string): Spec {
  const doc = parseDsl(additions);
  const ids = assignIds(
    doc.steps,
    existing.steps.length + 1,
    existing.steps.map((step) => step.id),
  );
  const newSteps = doc.steps.map((step, position) =>
    toSpecStep(step, existing.steps.length + position + 1, ids[position]),
  );
  const spec: Spec = {
    ...existing,
    steps: [...existing.steps, ...newSteps],
  };
  validateSpec(spec);
  return spec;
}

export {
  DslParseError,
  parseDsl,
  type DslDocument,
  type DslStep,
} from "./dsl.mts";

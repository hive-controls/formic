/**
 * Proposal validation and application — the enforcement point for the heal rules.
 *
 * A healer's output is DATA, parsed strictly here. It cannot reach the spec any other
 * way, which is what keeps an agent-backed healer as safe as a scripted one: whatever
 * the backend, the only thing it can change is what `applyProposal` lets it change.
 */
import { newStepId } from "../capture/recorder.mts";
import {
  checkAssertionFields,
  checkFrameChain,
  validateSpec,
} from "../spec/parse.mts";
import {
  ACTION_KINDS,
  SECRET_PLACEHOLDER_PATTERN,
  VALUE_FROM_FORM,
  VALUE_FROM_PATTERN,
  type Assertion,
  type Spec,
} from "../spec/types.mts";
import {
  PROPOSAL_KINDS,
  type ProposedStep,
  type RepairProposal,
} from "./types.mts";

export class ProposalValidationError extends Error {
  constructor(readonly problems: string[]) {
    super(`proposal is invalid:\n  - ${problems.join("\n  - ")}`);
    this.name = "ProposalValidationError";
  }
}

/**
 * A proposed assertion is held to the SPEC's own contract, not a thinner copy of it.
 *
 * There used to be a second, looser assertion validator here. It knew the mutual
 * exclusivity rule and the name/role rule and nothing else, so a proposal carrying a
 * relative `url`, a non-canonical one, an uncompilable `urlPattern` or a blank `testId`
 * was ACCEPTED at proposal time and only refused afterwards by `validateSpec`, from
 * inside `applyProposal` — where the reader is shown a spec error and has to work back
 * to the proposal that caused it. One validator, one contract, one refusal, at the
 * moment the proposal arrives.
 */
function checkAssertion(value: unknown, at: string, problems: string[]): void {
  checkAssertionFields(value, at, at, problems);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

/**
 * An inserted step's value half.
 *
 * A healer reads the whole spec, placeholders included, so the two mistakes worth
 * naming are quoting one back (`value: "<secret:password>"` — the literal that is not a
 * value) and offering both spellings at once. `valueFrom` itself is allowed and
 * encouraged: an interstitial that asks for a one-time code is exactly the flow change
 * the insert-step kind exists for, and it must be able to say where that code comes
 * from without inventing one.
 */
function checkProposedValue(
  value: Record<string, unknown>,
  problems: string[],
): void {
  if (value.value !== undefined && typeof value.value !== "string") {
    problems.push("step.value must be a string");
  }
  if (value.value !== undefined && value.valueFrom !== undefined) {
    problems.push(
      "step carries both value and valueFrom — a step's value is either written down or referenced, never both",
    );
  }
  if (
    typeof value.value === "string" &&
    SECRET_PLACEHOLDER_PATTERN.test(value.value)
  ) {
    problems.push(
      `step.value is a redaction placeholder, not a value — reference the real one with valueFrom: ${VALUE_FROM_FORM}`,
    );
  }
  if (
    value.valueFrom !== undefined &&
    (typeof value.valueFrom !== "string" ||
      !VALUE_FROM_PATTERN.test(value.valueFrom))
  ) {
    problems.push(
      `step.valueFrom must match ${VALUE_FROM_FORM}, got ${String(value.valueFrom)}`,
    );
  }
}

function checkProposedStep(value: unknown, problems: string[]): void {
  if (!isRecord(value)) {
    problems.push("step must be a mapping");
    return;
  }
  for (const key of Object.keys(value)) {
    if (
      !["action", "target", "frame", "value", "valueFrom", "assert"].includes(
        key,
      )
    ) {
      problems.push(
        `step.${key} is not allowed — a healer may set action, target, frame, value, valueFrom, assert`,
      );
    }
  }
  if (!ACTION_KINDS.includes(value.action as never)) {
    problems.push(`step.action must be one of ${ACTION_KINDS.join(" | ")}`);
  }
  if (!nonEmptyString(value.target)) problems.push("step.target is required");
  if (value.frame !== undefined)
    checkFrameChain(value.frame, "step.frame", problems);
  checkProposedValue(value, problems);
  if (value.assert !== undefined)
    checkAssertion(value.assert, "step.assert", problems);
}

/**
 * `rewrite-target: { stepId, target }` — the kind as the key, fields nested — is an
 * unambiguous shape a real agent produced (codex-cli 0.151.0, first live run). It is
 * accepted and normalised; everything else stays strict.
 */
function normalizeShape(raw: unknown): unknown {
  if (!isRecord(raw) || raw.kind !== undefined) return raw;
  const kinds = Object.keys(raw).filter((k) =>
    PROPOSAL_KINDS.includes(k as never),
  );
  if (kinds.length !== 1 || !isRecord(raw[kinds[0]])) return raw;
  const { [kinds[0]]: body, ...rest } = raw;
  return { kind: kinds[0], ...(body as Record<string, unknown>), ...rest };
}

/** Strict: unknown kinds and stray fields are rejected, never ignored. */
export function parseProposal(input: unknown): RepairProposal {
  const problems: string[] = [];
  const raw = normalizeShape(input);
  if (!isRecord(raw))
    throw new ProposalValidationError(["proposal must be a mapping"]);
  // "type" for "kind" is the one spelling models reach for; it names the same thing.
  if (!("kind" in raw) && typeof raw.type === "string") {
    raw.kind = raw.type;
    delete raw.type;
  }
  if (!PROPOSAL_KINDS.includes(raw.kind as never)) {
    throw new ProposalValidationError([
      `kind must be one of ${PROPOSAL_KINDS.join(" | ")}, got ${String(raw.kind)}`,
    ]);
  }
  if (!nonEmptyString(raw.reason)) problems.push("reason is required");
  // A model that names the step it could not repair is being helpful, not smuggling a
  // change: a no-repair touches nothing, so a stray stepId is dropped, not refused.
  if (raw.kind === "no-repair" && "stepId" in raw) delete raw.stepId;

  const allowed: Record<string, string[]> = {
    // `frame` rides with `target` and NOWHERE else: a chain is part of a step's
    // address, and this is the only kind that reaches an existing step's address.
    "rewrite-target": ["kind", "stepId", "target", "frame", "reason"],
    "insert-step": ["kind", "beforeStepId", "step", "reason"],
    "propose-assert-change": ["kind", "stepId", "to", "reason"],
    "no-repair": ["kind", "reason"],
  };
  for (const key of Object.keys(raw)) {
    if (!allowed[raw.kind as string].includes(key)) {
      // The rule this catches: a rewrite-target carrying `assert` is a repair trying
      // to change the expectation through the back door.
      problems.push(`${key} is not allowed on a ${String(raw.kind)} proposal`);
    }
  }

  switch (raw.kind) {
    case "rewrite-target":
      if (!nonEmptyString(raw.stepId)) problems.push("stepId is required");
      if (!nonEmptyString(raw.target)) problems.push("target is required");
      if (raw.frame !== undefined)
        checkFrameChain(raw.frame, "frame", problems);
      break;
    case "insert-step":
      if (!nonEmptyString(raw.beforeStepId))
        problems.push("beforeStepId is required");
      checkProposedStep(raw.step, problems);
      break;
    case "propose-assert-change":
      if (!nonEmptyString(raw.stepId)) problems.push("stepId is required");
      checkAssertion(raw.to, "to", problems);
      break;
  }
  if (problems.length > 0) throw new ProposalValidationError(problems);
  return raw as unknown as RepairProposal;
}

/**
 * A step that referenced its value still references it.
 *
 * The healer is the ONE actor that rewrites a committed spec, so it is the one that
 * could put a credential back into it. The four proposal kinds that exist today cannot
 * reach an existing step's value at all — this is what keeps that true of the fifth,
 * and it is checked on the OUTCOME rather than per kind for exactly that reason.
 */
export function demandReferencesKept(before: Spec, after: Spec): void {
  const problems: string[] = [];
  for (const original of before.steps) {
    if (original.valueFrom === undefined) continue;
    const repaired = after.steps.find((step) => step.id === original.id);
    if (repaired === undefined) continue;
    if (repaired.valueFrom !== original.valueFrom) {
      problems.push(
        `step ${original.id}: its value must stay referenced — the spec said ${original.valueFrom} and the repair says ${String(repaired.valueFrom)}`,
      );
    } else if (repaired.value !== undefined) {
      problems.push(
        `step ${original.id}: its value must stay referenced — the repair added a literal value beside ${original.valueFrom}`,
      );
    }
  }
  if (problems.length > 0) throw new ProposalValidationError(problems);
}

/**
 * Returns a NEW spec; the input is never mutated. The result is re-validated, so an
 * inserted state-changing step without an assertion is refused exactly as a captured
 * one would be — the class-4 rule holds for healed steps too.
 */
export function applyProposal(
  spec: Spec,
  proposal: RepairProposal,
  generateId: () => string = newStepId,
): Spec {
  const steps = spec.steps.map((step) => ({ ...step }));
  const find = (stepId: string) => {
    const step = steps.find((s) => s.id === stepId);
    if (!step)
      throw new ProposalValidationError([`no step with id "${stepId}"`]);
    return step;
  };

  switch (proposal.kind) {
    case "no-repair":
      break;
    case "rewrite-target": {
      const step = find(proposal.stepId);
      step.target = proposal.target;
      // Absent means "leave the chain alone", which is what almost every repair means.
      if (proposal.frame !== undefined) step.frame = proposal.frame;
      break;
    }
    case "insert-step": {
      const position = steps.indexOf(find(proposal.beforeStepId));
      const inserted: ProposedStep & { id: string; index: number } = {
        id: generateId(),
        index: 0,
        ...proposal.step,
      };
      steps.splice(position, 0, inserted);
      break;
    }
    case "propose-assert-change": {
      const step = find(proposal.stepId);
      // A COPY: sharing the object with `assert` makes the YAML serialiser emit an
      // anchor/alias pair (`&a1` / `*a1`), which is unreadable in a review diff.
      const from: Assertion = { ...(step.assert ?? {}) };
      // The assertion itself is untouched. This is the whole point.
      step.proposedAssertChange = {
        from,
        to: proposal.to,
        reason: proposal.reason,
      };
      break;
    }
  }

  steps.forEach((step, position) => {
    step.index = position + 1;
  });
  const repaired: Spec = { ...spec, steps };
  // BEFORE the validator, deliberately. A repair that puts a credential back into the
  // spec can also be malformed in some other way — an inserted step taking over a
  // referenced step's id is both — and the finding a reader needs is the credential,
  // not the symptom the validator would name first.
  demandReferencesKept(spec, repaired);
  validateSpec(repaired);
  return repaired;
}

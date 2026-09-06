/**
 * Assertion proposals — what a reviewer would SEE after a step, offered for confirmation.
 *
 * A recording must never write an assertion on its own. The spec format already has the
 * precedent: a machine may propose an expectation, only a human may adopt one
 * (`SpecStep.proposedAssertChange` — a repair that rewrites what a test expects turns a
 * caught bug into a green run). This applies the same rule at birth: recording derives
 * the LOCATOR (deterministic, from the DOM) and PROPOSES the EXPECTATION (a judgement,
 * from what changed on screen).
 *
 * The derivation is a pure function of two `VisibleState` snapshots the page itself
 * reported, so the same recording always proposes the same assertions — no page round
 * trip here, no model anywhere.
 */
import type { Assertion } from "../spec/types.mts";
import type { TargetFacts, VisibleNode, VisibleState } from "./events.mts";
import { preferredLocator } from "./events.mts";
import type { Spec } from "../spec/types.mts";

/** Which observation prompted the proposal — context for the human, never a claim about
 *  what the assertion checks. `summary` is the only thing that says that, and it is
 *  derived from the assertion itself. */
export type ProposalBasis = "url" | "appeared" | "value";

export interface AssertionProposal {
  /** The spec step this belongs to. Stable id, never a position. */
  stepId: string;
  basis: ProposalBasis;
  /**
   * What the PAGE saw become of this step's own target once the step had settled.
   *
   * Carried so a stand-in assertion can be true rather than merely well-shaped: a
   * state-changing click usually removes the thing it clicked, and asserting such a
   * target is visible fails on the very page it was recorded from.
   */
  targetAfter?: "visible" | "gone";
  /**
   * The document URL this step ARRIVED at, when the step changed it.
   *
   * `location.href` as the page reported it, which is canonical by construction — the
   * form the grammar requires of a `url` assertion, and the form every browser reports.
   */
  urlAfter?: string;
  /**
   * One line, for the confirmation prompt and the recording's own log — DERIVED from
   * `assertion`, so the sentence the human approves and the check the spec commits are
   * the same statement with one source.
   *
   * They used to be written separately, and drifted exactly where it mattered: a fill
   * was summarised as `#email holds "ops@example.test"` while the assertion it committed
   * was only that `#email` is visible, so the human approved a value check the replay
   * never made. The grammar cannot express an input's value; the proposal now says what
   * it can express, and nothing more.
   */
  summary: string;
  assertion: Assertion;
}

/** What the recorder knows about the step an observation belongs to. */
export interface ProposedFor {
  stepId: string;
  action: string;
  target: string;
  targetFacts: TargetFacts;
}

/** How the assertion's locator names the element, in the reader's terms. */
function describeLocator(assertion: Assertion): string {
  if (assertion.testId !== undefined)
    return `[data-testid="${assertion.testId}"]`;
  if (assertion.selector !== undefined) return assertion.selector;
  if (assertion.role !== undefined) {
    return assertion.name === undefined
      ? `the ${assertion.role}`
      : `the ${assertion.role} "${assertion.name}"`;
  }
  return `the text "${assertion.text}"`;
}

/**
 * The assertion, in a sentence — every field it carries and no field it does not.
 *
 * This is the whole of ruling: a confirmation prompt that promises more than the
 * assertion enforces gets a yes for a check that will never run.
 */
export function describeAssertion(assertion: Assertion): string {
  const where = describeLocator(assertion);
  const visibility = assertion.visible === false ? "is hidden" : "is visible";
  if (assertion.hasText !== undefined)
    return `${where} ${visibility} and reads "${assertion.hasText}"`;
  if (assertion.containsText !== undefined)
    return `${where} ${visibility} and contains "${assertion.containsText}"`;
  return `${where} ${visibility}`;
}

/**
 * An assertion on a node the page reported as visible, in the same field precedence a
 * target locator is derived with — or null when the node cannot be named at all.
 *
 * Two facts about the node decide the PREDICATE, and both ride on the record the page
 * already made rather than being re-derived here:
 *
 *  - a CLASSIFIED node is asserted visible and nothing more. Its content is what the
 *    value channel withheld, and an assertion is a projection like any other: quoting it
 *    back would commit the secret to the spec through the one field nobody was watching.
 *  - TRUNCATED text is `containsText`, never exact `hasText`. The page caps what it
 *    reports, and an exact match on a prefix is an assertion that fails on the very page
 *    it was recorded from.
 */
function assertionOn(node: VisibleNode): Assertion | null {
  let locator: Assertion;
  try {
    locator = preferredLocator({
      testId: node.testId,
      // A heading is the one structural fact the page reports; the ROLE is named here,
      // where the assertion is built, rather than by a second accessibility model in
      // the page that called every input a textbox.
      role: node.heading === true ? "heading" : undefined,
      name:
        node.heading === true && !node.classification ? node.text : undefined,
      // Text is a locator only when the page proved it names ONE element. Otherwise it
      // is something to display, and a node with nothing else to be addressed by is not
      // proposed at all — a refusal beats an assertion that resolves two elements.
      text:
        node.classification || node.textLocator !== true
          ? undefined
          : node.text || undefined,
    });
  } catch {
    return null;
  }
  if (node.classification) return { ...locator, visible: true };
  if (!node.text) return locator;
  return node.truncated
    ? { ...locator, containsText: node.text }
    : { ...locator, hasText: node.text };
}

/** A node's identity for "did this appear?" — text included, because a testId whose
 *  TEXT changed (the signed-in user's name landing in an always-present slot) is the
 *  most common thing a step actually proves. */
function signature(node: VisibleNode): string {
  return `${node.testId ?? ""}|${node.heading === true ? "heading" : ""}|${node.text}`;
}

function firstAppeared(
  before: VisibleState,
  after: VisibleState,
): VisibleNode | undefined {
  const known = new Set(before.nodes.map(signature));
  return after.nodes.find((node) => !known.has(signature(node)));
}

function firstHeading(state: VisibleState): VisibleNode | undefined {
  return state.nodes.find((node) => node.heading === true && node.text !== "");
}

/**
 * Propose what to assert after `step`, from the page state before it and after it.
 *
 * Always returns something for a state-changing action: the spec format requires an
 * assertion there, so a step with nothing to propose would be a spec that cannot load.
 * The fallback — the element the step acted on, still visible — is the weakest true
 * statement about the step, and it is offered as such.
 */
export function proposeAssertion(
  step: ProposedFor,
  before: VisibleState,
  after: VisibleState,
): AssertionProposal | null {
  const propose = (
    basis: ProposalBasis,
    assertion: Assertion | null,
  ): AssertionProposal | null =>
    assertion === null
      ? null
      : {
          stepId: step.stepId,
          basis,
          summary: describeAssertion(assertion),
          assertion,
        };
  // The element the step ACTED ON, still on screen. Never valid across a navigation —
  // that element lives on the document the navigation left behind.
  const stillThere = (): Assertion | null => {
    if (step.targetFacts.classification) {
      const { text: _text, name: _name, ...rest } = step.targetFacts;
      try {
        return { ...preferredLocator(rest), visible: true };
      } catch {
        return null;
      }
    }
    try {
      return { ...preferredLocator(step.targetFacts), visible: true };
    } catch {
      return null;
    }
  };

  // The grammar has no predicate for an input's VALUE, so a fill's proposal commits the
  // only thing it can say truthfully — the field is on screen — and says exactly that.
  if (step.action === "fill" || step.action === "select") {
    return propose("value", stillThere());
  }
  if (after.url !== before.url) {
    const heading = firstHeading(after);
    if (heading) return propose("url", assertionOn(heading));
    const arrived = firstAppeared(before, after);
    // Nothing on the arrived page can be named, and the page the step acted on is gone.
    // There is no true statement to offer, so none is offered: the step stays bare and
    // the validator says so, which is the outcome the confirmation pass exists for.
    return arrived ? propose("url", assertionOn(arrived)) : null;
  }
  const appeared = firstAppeared(before, after);
  if (appeared) return propose("appeared", assertionOn(appeared));
  return propose("appeared", stillThere());
}

/**
 * Write the ACCEPTED proposals into the spec. Nothing else may set `assert` on a
 * recorded step — a proposal the human skipped simply leaves the step bare, and the
 * spec validator refuses it, which is the outcome the confirmation pass is for.
 */
export function applyProposals(
  spec: Spec,
  accepted: readonly AssertionProposal[],
): Spec {
  const byStep = new Map(accepted.map((one) => [one.stepId, one.assertion]));
  return {
    ...spec,
    steps: spec.steps.map((step) => {
      const assertion = byStep.get(step.id);
      return assertion ? { ...step, assert: assertion } : step;
    }),
  };
}

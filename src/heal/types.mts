/**
 * The heal seam — same shape as the driver seam. One interface, several backends
 * (an OpenAI-compatible endpoint, a headless agent CLI, a scripted fake), and the
 * harness above this line never learns which one proposed a repair.
 *
 * What a healer may and may not do is encoded in the PROPOSAL type, not in prose:
 *  - it can rewrite one step's `target`, or insert one step (breakage classes 1 and 3);
 *  - it can PROPOSE an assertion change, which a human accepts in the PR — never apply
 *    one (class 4: rewriting the expectation turns a caught bug into a green test);
 *  - it can decline.
 * Every applied proposal is re-verified by replaying the whole spec before it counts.
 */
import type { ReplayFailure } from "../replay/runner.mts";
import type {
  ActionKind,
  Assertion,
  FrameChain,
  Spec,
  SpecStep,
} from "../spec/types.mts";

export interface HealContext {
  spec: Spec;
  failure: ReplayFailure;
  failedStep: SpecStep;
  /** Live page state at the moment of failure. */
  url: string;
  /** Playwright's accessibility snapshot of the page body — what the user can see. */
  ariaSnapshot: string;
  /** 1-based; earlier attempts are listed so a healer does not repeat itself. */
  attempt: number;
  priorAttempts: PriorAttempt[];
}

export interface PriorAttempt {
  proposal: RepairProposal;
  /** Why it did not count: the replay's failure after applying it. */
  result: string;
}

/** The step a healer may insert. `id` and `index` are assigned by the harness. */
export interface ProposedStep {
  action: ActionKind;
  target: string;
  /** The nested browsing context the new step's target is addressed in. An inserted
   *  interstitial can live in a frame exactly as a recorded step can. */
  frame?: FrameChain;
  value?: string;
  /** Where the value comes from, for a step that needs one the spec must not carry —
   *  an interstitial asking for a one-time code is the case this exists for. */
  valueFrom?: string;
  assert?: Assertion;
}

export type RepairProposal =
  | {
      kind: "rewrite-target";
      stepId: string;
      target: string;
      /**
       * A new frame chain for the step, when the locator moved INTO or OUT OF a frame.
       *
       * A frame chain is part of a step's address, not of its expectation — so it may be
       * repaired, and only here. `rewrite-target` is the one kind that reaches an
       * existing step's address at all: `insert-step` creates a step, and
       * `propose-assert-change` writes into `proposedAssertChange` and never touches the
       * step itself. Omitted, the step keeps the chain it has; `[]` is refused by the
       * validator, so "move it back to the page" is spelled out as a proposal a reviewer
       * can read, not as an empty list nobody notices.
       */
      frame?: FrameChain;
      reason: string;
    }
  | {
      kind: "insert-step";
      /** The new step runs immediately BEFORE this one. */
      beforeStepId: string;
      step: ProposedStep;
      reason: string;
    }
  | {
      kind: "propose-assert-change";
      stepId: string;
      to: Assertion;
      reason: string;
    }
  | { kind: "no-repair"; reason: string };

export const PROPOSAL_KINDS = [
  "rewrite-target",
  "insert-step",
  "propose-assert-change",
  "no-repair",
] as const;

export interface Healer {
  /** Backend name for the audit record, e.g. "openai-compatible", "agent:claude". */
  readonly name: string;
  /** What acted — a model id or an agent CLI version. Lands in modelVersion. */
  readonly modelVersion: string;
  /** The model a profile named for this healer, if any — null when none was requested.
   *  Unset on healer kinds with no such ambiguity (e.g. openai-compatible, where
   *  modelVersion is already the resolved model). */
  readonly modelRequested?: string | null;
  /** True once modelRequested was actually passed to the backend (argv or env); the
   *  label/audit must never claim a model ran when this is false. */
  readonly modelPassed?: boolean;
  propose(context: HealContext): Promise<RepairProposal>;
}

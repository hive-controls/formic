export {
  Recorder,
  newStepId,
  type ObservedStep,
  type RecordedRun,
} from "./recorder.mts";
export {
  recordFlow,
  type RecordFlowOptions,
  type RecordedFlow,
} from "./record.mts";
export {
  attachCaptureListeners,
  captureBundleSource,
  preferredLocator,
  readVisibleState,
  targetSelectorFor,
  CAPTURE_BINDING,
  VISIBLE_STATE_HOOK,
  type CapturedEvent,
  type CapturedEventKind,
  type TargetFacts,
  type VisibleNode,
  type VisibleState,
} from "./events.mts";
export {
  applyProposals,
  proposeAssertion,
  type AssertionProposal,
  type ProposalBasis,
  type ProposedFor,
} from "./proposals.mts";

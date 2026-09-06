/**
 * Recording a human's flow: the browser event stream → a spec.
 *
 * The host half of capture/events.mts. It opens nothing and closes nothing — it is
 * handed an open session, so it works on whatever gate the Fleet chose, Outside or
 * Inside, without knowing which.
 *
 * Four rules make a human recording produce the same artifacts a scripted one does:
 *
 *  - EVERY step goes through `Recorder.observe`, so the step log is timed by the same
 *    `runTimedStep` and anchored in the same recorder clock. The evidence slicer needs
 *    no change to address a human's clicks.
 *  - A step is anchored at the moment the PAGE stamped the event, not at the moment the
 *    host's handler ran. The binding hop is a process boundary, and a window that opens
 *    after it starts too late: the interaction that caused the step lands in the
 *    PREVIOUS step's window, which is the wrong-evidence-window defect this repo has
 *    already paid for once in the clock domain.
 *  - A step's window closes when the NEXT step's event arrives. A click's consequences —
 *    the navigation it caused, the form it submitted — belong to the click that caused
 *    them.
 *  - A CONSEQUENCE IS FOLDED, AN ACTION IS NOT. A navigation inside a click's settle
 *    window is that click's consequence and folds into it. A navigation with no action
 *    before it is the human typing an address, and becomes its own `goto` — dropping it
 *    would write a spec that skips a whole page. A form submitted with no submitter was
 *    submitted from the keyboard, and becomes a `press Enter`.
 */
import type { DriverSession } from "../driver/types.mts";
import type { ActionKind } from "../spec/types.mts";
import { resolveFrameChain, type LocatorScope } from "../replay/frames.mts";
import type { CapturedEvent, TargetFacts, VisibleState } from "./events.mts";
import {
  attachCaptureListeners,
  proveTargetFacts,
  readDocumentMarks,
  readFrameId,
  readVisibleState,
  targetSelectorFor,
} from "./events.mts";
import {
  secretVariableName,
  type SensitiveCategory,
  type SensitiveClassification,
} from "./sensitive-fields.mts";
import { Recorder, type ObservedStep, type RecordedRun } from "./recorder.mts";
import { proposeAssertion, type AssertionProposal } from "./proposals.mts";

export interface RecordFlowOptions {
  /** An OPEN session from any gate. Never opened or closed here. */
  session: DriverSession;
  specName: string;
  driverName: string;
  /** Where the recording starts. Becomes the spec's first `goto` step. */
  startUrl: string;
  /** Resolves when the human ends the recording (a key press at the CLI). */
  stop: Promise<void>;
  /** Record password values verbatim instead of `<secret>`. */
  includeSecrets?: boolean;
  /** Injectable so tests can assert on stable ids. */
  generateId?: () => string;
  /** Where a refusal is written down. `recordFlow` supplies it. */
  warnings?: string[];
}

/** A step whose value was withheld because the field holds personal or secret data.
 *  Named — and categorised — so the CLI can tell the human which step a replay will need
 *  a real value for, and what kind of value that is. */
export interface WithheldSecret {
  stepId: string;
  index: number;
  target: string;
  category: SensitiveCategory;
  /** The environment variable the step now references, without the `env.` scheme —
   *  what the human has to set before this spec can replay. */
  variable: string;
}

export interface RecordedFlow extends RecordedRun {
  /** One per human step, in order. Nothing here is in the spec yet. */
  proposals: AssertionProposal[];
  /** Steps recorded as `<secret>`. Empty when nothing sensitive was typed. */
  secrets: WithheldSecret[];
  /** Everything the recording could NOT represent, said out loud. A recording that
   *  silently drops what the human did is worse than one that refuses: they would read
   *  a spec missing an action and have no way to know it. */
  warnings: string[];
}

/** Events arrive from a page binding whenever the human acts; the recorder consumes them
 *  one at a time. A queue with a single waiter is the whole of it. */
interface EventPump {
  push(event: CapturedEvent): void;
  /** The next event, or `undefined` once the human has stopped and the queue is drained. */
  next(): Promise<CapturedEvent | undefined>;
}

/** How far along a particular document had got when the recording stopped. */
interface DocumentMark {
  frameId: string;
  seq: number;
}

/** Whether EVERY document open when the recording stopped has delivered what it sent.
 *  Child frames included: a human's last action is as likely to be inside an iframe as
 *  on the page, and a barrier that watched one document ended the drain on the other. */
function drained(
  delivered: Map<string, number>,
  highWater: DocumentMark[] | null,
): boolean {
  if (highWater === null) return true;
  return highWater.every(
    (mark) => (delivered.get(mark.frameId) ?? 0) >= mark.seq,
  );
}

/** How long the drain will wait for traffic the page said it sent. Time BOUNDS the wait;
 *  it never decides anything. A page that closed mid-hop would otherwise hang a stop. */
const DRAIN_DEADLINE_MS = 5000;

/** How long the host waits for the frame a just-reported event came from. The chain was
 *  built from a LIVE frame moments earlier, so this only bounds a frame that detached
 *  between the two — it never decides anything. */
const PROOF_TIMEOUT_MS = 2000;

/**
 * The pump, with a STOP BARRIER.
 *
 * Stop used to make `next()` return `undefined` the moment the queue was empty, so an
 * event still crossing the binding was queued with no consumer left to take it: the
 * human's last action vanished, and a fixed sleep in the tests was the only thing
 * hiding it — production had no sleep at all.
 *
 * `settle` is what closes that gap. It takes the listeners off the page FIRST, so no new
 * event can be produced, and then reads how much the page had already sent. The pump
 * ends only once it has delivered that much — or once the deadline says the rest is
 * never coming, which is the only thing a clock is used for here.
 */
export function createEventPump(
  stop: Promise<void>,
  settle: () => Promise<DocumentMark[]>,
  onDiscard: (warning: string) => void = () => {},
): EventPump {
  const queue: CapturedEvent[] = [];
  let stopped = false;
  // PER DOCUMENT. Event numbering restarts in every one of them, so a single global
  // maximum let a busy first page satisfy the stop condition for a quiet second one —
  // and the human's last action, on the page they ended on, was exactly what the
  // barrier existed to save.
  let highWater: DocumentMark[] | null = null;
  const delivered = new Map<string, number>();
  let wake: (() => void) | null = null;
  const ring = () => {
    const waiter = wake;
    wake = null;
    waiter?.();
  };
  void stop.then(async () => {
    highWater = await settle();
    stopped = true;
    ring();
    setTimeout(() => {
      highWater = null;
      ring();
    }, DRAIN_DEADLINE_MS).unref?.();
  });
  return {
    push(event) {
      // PAST THE MARK is past the stop. The barrier records how much each document had
      // already sent when the human stopped; anything beyond that number was produced
      // afterwards, and the drain exists to deliver what was in flight, not to keep
      // recording. Teardown reaches every document now, so this is the second lock on
      // the same door — it also covers an event produced between the teardown call and
      // its completion in a frame.
      if (highWater !== null) {
        const mark = highWater.find((one) => one.frameId === event.frameId);
        // A document the barrier never saw is a document that arrived AFTER the stop —
        // it has no mark to be past, so the gate above could not hold it and it walked
        // straight into the spec. Said out loud rather than silently dropped: a
        // recording that is short and says why beats one that is short and does not.
        if (mark === undefined) {
          onDiscard(
            `an interaction after the recording stopped, in a document that opened during the drain, was not recorded`,
          );
          return;
        }
        if (event.seq > mark.seq) return;
      }
      queue.push(event);
      const seen = delivered.get(event.frameId) ?? 0;
      if (event.seq > seen) delivered.set(event.frameId, event.seq);
      ring();
    },
    async next() {
      for (;;) {
        const head = queue.shift();
        if (head !== undefined) return head;
        if (stopped && drained(delivered, highWater)) return undefined;
        await new Promise<void>((resolve) => {
          wake = resolve;
        });
      }
    },
  };
}

/**
 * Which replay action reproduces this event, decided by the control's TYPE.
 *
 * The element decides, never the event name: `input` fires for a `<select>` and for a
 * checkbox too, and a spec that answered "fill" for either would carry a step the
 * runner cannot execute — `page.fill` refuses both. The bundle already withholds the
 * events that have no step of their own (a checkbox's, folded into its click), so what
 * arrives here is only ever a control a value can be written to.
 */
function actionFor(event: CapturedEvent): ActionKind {
  if (event.kind === "click") return "click";
  if (event.kind === "submit") return "press";
  if (event.kind === "navigation") return "goto";
  return event.control === "select" ? "select" : "fill";
}

/** The value a step carries. A withheld secret has none — the step gets a `valueFrom`
 *  reference instead (see `referenceFor`), so nothing here has to invent one. */
function valueFor(event: CapturedEvent): string {
  return event.value ?? "";
}

/** The short human word for a field, for the variable name the human will set. In the
 *  order that names it best: the id the app chose for testing, then the accessible name
 *  the page shows, then the id a `#id` selector carries. */
function fieldLabelOf(target: TargetFacts): string | undefined {
  if (target.testId !== undefined) return target.testId;
  if (target.name !== undefined) return target.name;
  const id = /^#([\w-]+)$/.exec(target.selector ?? "");
  return id?.[1];
}

/** The classification of a value the page decided not to send, or undefined when it
 *  sent one — including when `--include-secrets` made it send a classified one. */
function withheldBy(event: CapturedEvent): SensitiveClassification | undefined {
  return event.value === undefined ? event.classification : undefined;
}

function draftFor(
  event: CapturedEvent,
  valueFrom: string | undefined,
): ObservedStep {
  const action = actionFor(event);
  // A goto navigates the PAGE — the grammar refuses a frame on one, and a navigation
  // reported by a child document is a document load, not a step the human took.
  if (action === "goto") return { action, target: event.url };
  const target = targetSelectorFor(event.target ?? {});
  const frame = event.frame === undefined ? {} : { frame: event.frame };
  if (action === "click") return { action, target, ...frame };
  if (action === "press") return { action, target, ...frame, value: "Enter" };
  // A referenced value and a written-down one are exclusive — the grammar says so, and
  // the step carries whichever one this field earned.
  if (valueFrom !== undefined) return { action, target, ...frame, valueFrom };
  return { action, target, ...frame, value: valueFor(event) };
}

/** Keystroke coalescing: consecutive typing into the SAME field is one fill step whose
 *  value is the last one seen. Without this a five-character name is five steps, none of
 *  which describes what the human did. */
function coalesces(
  draft: ObservedStep,
  documentId: string,
  event: CapturedEvent,
): boolean {
  if (draft.action !== "fill" && draft.action !== "select") return false;
  if (event.kind !== "input" && event.kind !== "change") return false;
  if (event.target === undefined) return false;
  if (actionFor(event) !== draft.action) return false;
  // The SAME field, which means the same selector in the same DOCUMENT — the document's
  // own id, not the chain that names it. Two frames of one embedded form carry the same
  // `#code`, and so do the same frame's documents either side of a navigation: typing
  // before and after one is two fills with the navigation between them, and folding them
  // wrote a spec that lost the first value entirely.
  if (event.frameId !== documentId) return false;
  return targetSelectorFor(event.target) === draft.target;
}

/**
 * Whether a navigation is the in-flight action's CONSEQUENCE.
 *
 * Elapsed time used to answer this, and it answered wrong in both directions: click an
 * inert control, then go somewhere by hand two seconds later, and the goto disappeared;
 * let a redirect chain take three seconds and the click's real consequence became a
 * redundant goto in the middle of the spec.
 *
 * Nothing here is a clock. A same-document route change names the action whose
 * activation was in flight when it happened. A new document carries the browser's own
 * answer instead — `document.referrer` is set when the document before it initiated the
 * navigation, however long that took, and empty when the human typed an address.
 */
function foldsInto(
  causeActionId: string | undefined,
  event: CapturedEvent,
): boolean {
  // IDENTITY, and nothing else. The referrer said a load was page-initiated but never
  // WHICH action initiated it, so a slow consequence of A was handed to whatever was
  // open when it landed, and a policy of `no-referrer` split a real click's navigation
  // into a redundant goto. Both are answered by the id the activation carried.
  //
  // Any action may own a route, not only a click: a select whose change navigates is
  // the same shape of fact. An event with no cause is the human's own move.
  return causeActionId !== undefined && event.causeActionId === causeActionId;
}

/** Drain the consequences of an action that was NOT recorded, so the next real action
 *  starts clean rather than inheriting a navigation it did not cause. */
async function settleUnrecorded(
  pump: EventPump,
): Promise<CapturedEvent | undefined> {
  for (;;) {
    const event = await pump.next();
    if (event === undefined) return undefined;
    if (event.kind === "navigation" && event.referred === true) continue;
    if (event.kind === "submit" && event.submitter === true) continue;
    return event;
  }
}

/** What settling a step produced: the event that ended it (if any), and the page state
 *  the consequences left behind. */
interface Settled {
  next?: CapturedEvent;
  folded?: VisibleState;
}

/** Hold the step's window open until something that is a step of its own arrives. */
async function settleStep(
  pump: EventPump,
  draft: ObservedStep,
  documentId: string,
  openingActionId: string | undefined,
): Promise<Settled> {
  const settled: Settled = {};
  // A coalesced event becomes part of THIS step, so its id becomes this step's id too —
  // a select's change is what a route it triggers will point back at, not the event that
  // opened the window.
  let causeActionId = openingActionId;
  for (;;) {
    const event = await pump.next();
    if (event === undefined) return settled;
    if (event.kind === "navigation") {
      // A frame's own load is not a step and not a consequence to fold into one: its
      // state describes a document this step may not even be in. It ends the step's
      // window, which is exactly right — whatever comes next is a new document.
      if (event.nested === true) return { ...settled, next: event };
      if (!foldsInto(causeActionId, event)) return { ...settled, next: event };
      settled.folded = event.state;
      continue;
    }
    // A submitter means a control was activated, and that activation is already a click
    // step; the submit itself adds nothing. Without one the human pressed Enter, which
    // is an action no other event describes.
    if (event.kind === "submit") {
      if (event.submitter === true) continue;
      return { ...settled, next: event };
    }
    if (coalesces(draft, documentId, event)) {
      // A coalesced event is more typing into the SAME field, so it carries the same
      // classification: a step that earned a reference keeps it, and must not gain a
      // literal value beside it.
      if (draft.valueFrom === undefined) draft.value = valueFor(event);
      if (event.actionId !== undefined) causeActionId = event.actionId;
      continue;
    }
    return { ...settled, next: event };
  }
}

/**
 * The recording's own `goto` announces itself as a navigation on the page it just
 * loaded; that announcement is the step already recorded, not a second one.
 *
 * It is recognised by CAUSE, not by url. Comparing urls discarded every same-url
 * navigation before the first action, so a human who reloaded or revisited the starting
 * page was confused with the recorder opening it. The host caused exactly one
 * navigation, it knows which, and it consumes exactly that one.
 */
async function firstAction(
  pump: EventPump,
  hostNavigation: { pending: boolean; frameId: string },
): Promise<CapturedEvent | undefined> {
  for (;;) {
    const event = await pump.next();
    if (event === undefined) return undefined;
    // By IDENTITY, not by arrival order. Consuming "the first navigation" raced the
    // binding hop: when the recorder's own announcement was still crossing and the human
    // navigated immediately, the HUMAN's goto was swallowed and a whole document vanished
    // from the spec. The host knows which document it loaded, so it consumes that one.
    if (
      event.kind === "navigation" &&
      hostNavigation.pending &&
      (hostNavigation.frameId === "" ||
        event.frameId === hostNavigation.frameId)
    ) {
      hostNavigation.pending = false;
      continue;
    }
    if (event.kind === "submit" && event.submitter === true) continue;
    return event;
  }
}

export async function recordFlow(
  options: RecordFlowOptions,
): Promise<RecordedFlow> {
  // The barrier: take the listeners off the page, then ask it how much it had already
  // sent. Assigned after `attachCaptureListeners` because it needs the handle, and read
  // only once the human has stopped.
  const barrier = {
    settle: async (): Promise<DocumentMark[]> => [],
  };
  const warnings: string[] = [];
  const pump = createEventPump(
    options.stop,
    () => barrier.settle(),
    (warning) => {
      if (!warnings.includes(warning)) warnings.push(warning);
    },
  );
  const capture = await attachCaptureListeners(
    options.session.page,
    (event) => pump.push(event),
    {
      includeSecrets: options.includeSecrets,
      onRefusal: (warning) => warnings.push(warning),
    },
  );
  const recorder = new Recorder(
    options.session,
    options.specName,
    options.driverName,
    options.generateId,
  );
  barrier.settle = async () => {
    // Listeners off FIRST, so nothing new can be produced, and only then ask the page
    // how much it had already sent. The sink stays until the drain is done — removing
    // it here would throw away the very events the barrier exists to wait for.
    await capture.stopListening();
    // Then let everything that has already crossed the binding reach the sink: naming a
    // child frame is a round trip, so a received event is not yet a delivered one, and a
    // mark read before that would be compared against a delivery count still climbing.
    await capture.settleDelivery();
    // Every document the page holds, each named and counted in ONE read — two reads
    // could be split by a navigation and pair an old id with a new low sequence.
    return readDocumentMarks(options.session.page);
  };
  const hostNavigation = { pending: true, frameId: "" };
  try {
    await recorder.goto(options.startUrl);
    // Read AFTER the navigation has loaded, so this is the document the host just
    // brought up and its announcement is the one to discard.
    hostNavigation.frameId = await readFrameId(options.session.page);
    const { proposals, secrets } = await consumeEvents(
      pump,
      recorder,
      { ...options, warnings },
      hostNavigation,
    );
    return { ...recorder.finish(), proposals, secrets, warnings };
  } finally {
    await capture.detach();
  }
}

/** The recording loop: one step per event that is an ACTION, held open until the next
 *  one arrives, with each step's proposal derived from the states either side of it. */
async function consumeEvents(
  pump: EventPump,
  recorder: Recorder,
  options: RecordFlowOptions,
  hostNavigation: { pending: boolean; frameId: string },
): Promise<{ proposals: AssertionProposal[]; secrets: WithheldSecret[] }> {
  const proposals: AssertionProposal[] = [];
  const secrets: WithheldSecret[] = [];
  const warnings = options.warnings ?? [];
  // Every variable name this recording has already claimed, so a second sensitive field
  // gets a name of its own rather than overwriting the first one's value.
  const variables = new Set<string>();
  let pending = await firstAction(pump, hostNavigation);
  while (pending !== undefined) {
    const event = pending;
    // A frame's own load is counted, so the stop barrier is satisfied, and then dropped:
    // the grammar's `goto` navigates the page, and a step made of this one drove the
    // WHOLE page to the frame's src. An event the binding could not address made no step
    // either — the refusal the human reads was written where the address failed.
    if (
      event.nested === true &&
      (event.kind === "navigation" || event.frame === undefined)
    ) {
      pending = await pump.next();
      continue;
    }
    // WHERE the event happened, as a live scope. The page proved every CSS candidate it
    // emitted, but it proved them in ITS OWN document — so the role/name proof has to
    // run there too, or a `#code` inside an iframe would be checked for uniqueness
    // against the host page, which does not contain it at all.
    let scope: LocatorScope;
    try {
      scope = await resolveFrameChain(
        options.session.page,
        event.frame,
        PROOF_TIMEOUT_MS,
      );
    } catch (gone) {
      // The frame was named from a live one moments ago and has since detached. Said out
      // loud rather than proven against the host page, which does not contain the
      // element and would emit a locator that resolves nothing.
      warnings.push(
        `a ${event.kind} inside a nested frame was not recorded — ${(gone as Error).message}`,
      );
      pending = await settleUnrecorded(pump);
      continue;
    }
    const targetFacts: TargetFacts =
      event.target === undefined
        ? {}
        : await proveTargetFacts(scope, event.target);
    // A withheld value never lands in the spec. The step names the environment
    // variable it will be read back from instead, derived here — where the field's own
    // label is still in hand — and unique across the recording.
    const withheld = withheldBy(event);
    const variable =
      withheld === undefined
        ? undefined
        : secretVariableName(
            options.specName,
            withheld.category,
            fieldLabelOf(targetFacts),
            variables,
          );
    let draft: ObservedStep;
    try {
      draft = draftFor(
        { ...event, target: targetFacts },
        variable === undefined ? undefined : `env.${variable}`,
      );
    } catch (unaddressable) {
      // Refused, by name. A recording that silently skips what the human did is worse
      // than one that is short and says which action it could not write down.
      warnings.push(
        `a ${event.kind} on <${event.tagName ?? "?"}> could not be addressed by any locator that resolves — the step was not recorded (${(unaddressable as Error).message})`,
      );
      pending = await settleUnrecorded(pump);
      continue;
    }
    // Claimed only once the step is real: a refused step above took no name with it.
    if (variable !== undefined) variables.add(variable);
    const before = event.state;
    let settled: Settled = {};
    const stepId = await recorder.observe(
      draft,
      async () => {
        settled = await settleStep(pump, draft, event.frameId, event.actionId);
      },
      event.timestamp,
    );
    if (withheld !== undefined && variable !== undefined) {
      secrets.push({
        stepId,
        index: recorder.stepCount,
        target: draft.target,
        category: withheld.category,
        variable,
      });
    }
    // The next event carries the page state as it was BEFORE the next action — this
    // step's end state, read with no race. A step that ended the recording has no such
    // event, and one whose consequence was a navigation has the arriving page's.
    // The next event carries the page state as it was BEFORE the next action — this
    // step's end state, read with no race. That only holds while the two are in the SAME
    // document: a click inside an iframe is usually followed by one on the host page, and
    // its state describes the host page, so an assertion derived from it would say
    // something true about a document this step never touched. Where the documents
    // differ — and where the recording simply ended — the step's own scope is read
    // instead, which is the same fallback the last step has always used.
    const nextInSameDocument =
      settled.next !== undefined && settled.next.frameId === event.frameId;
    const after = nextInSameDocument
      ? (settled.next as CapturedEvent).state
      : (settled.folded ?? (await readVisibleState(scope)));
    // A `goto` asserts by its own nature — the spec grammar exempts it, and proposing
    // an expectation for it would ask the human to confirm the navigation they typed.
    if (draft.action !== "goto") {
      // A step with nothing true to say about it proposes NOTHING, and stays bare. The
      // validator names it, which is the outcome the confirmation pass exists for —
      // strictly better than an assertion that can only fail.
      const proposal = proposeAssertion(
        { stepId, action: draft.action, target: draft.target, targetFacts },
        before,
        after,
      );
      if (proposal !== null) {
        // What the page saw become of this step's own target. A new document cannot
        // contain the old element, so a changed URL is "gone" by construction — that is
        // not an inference about page state, it is what a new document means.
        const navigated = after.url !== before.url;
        const targetAfter: "visible" | "gone" =
          navigated || after.actedStillVisible === false ? "gone" : "visible";
        proposals.push({
          ...proposal,
          targetAfter,
          ...(navigated ? { urlAfter: after.url } : {}),
        });
      }
      // A name the page refused to claim is said OUT LOUD. Silently shortening it would
      // have produced a prefix, and a prefix of an exact name matches nothing at all —
      // a spec that fails on the page it was recorded from, with no clue why.
      if (after.nodes.some((node) => node.nameTooLong === true)) {
        warnings.push(
          `step ${recorder.stepCount}: a heading's name is too long to assert on and was not claimed — assert on a data-testid instead`,
        );
      }
    }
    pending = settled.next;
  }
  return { proposals, secrets };
}

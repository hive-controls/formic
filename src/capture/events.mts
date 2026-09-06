/**
 * Browser-side event capture — the listener bundle a human's clicks flow through.
 *
 * Injected exactly the way rrweb is (driver/rrweb-recorder.mts: `addInitScript` for the
 * code, `exposeBinding` for the way out), so a recording rides the same seam every gate
 * already hands back and works Outside and Inside alike.
 *
 * Three properties make a human-driven recording as deterministic as a scripted one:
 *
 *  1. LOCATORS ARE DERIVED, NOT CHOSEN, AND STRUCTURED, NOT INTERPOLATED. The bundle
 *     reports plain FACTS about the event's target (its testId, a stable id selector,
 *     its role and accessible name, its text, and a positional CSS path of last resort)
 *     and the host picks a locator from them by `LOCATOR_PRECEDENCE`. The assertion the
 *     spec carries is the STRUCTURED field — `{ role, name, exact }`, never a selector
 *     string built by pasting page text into Playwright's engine syntax, where one
 *     quotation mark in a product name changes which element resolves. The step's own
 *     `target` is a string because the grammar says so, and there it is CSS only, with
 *     attribute values escaped losslessly.
 *  2. TIMESTAMPS COME FROM THE PAGE. Every event is stamped with the browser's own
 *     `Date.now()` — the clock the rrweb stream is stamped in — and that stamp, not the
 *     moment the host's handler ran, anchors the step. The binding hop is a hop across
 *     a process boundary; it must not move the evidence window.
 *  3. SECRETS DO NOT CROSS THE BINDING. A password field's value is withheld page-side,
 *     before transmission, unless the human opted in — see `CaptureOptions`.
 *
 * The listeners run in the CAPTURE phase on `document`, so the state each event carries
 * is the state BEFORE the application's own handlers change anything. That is what lets
 * the host propose an assertion by comparing one event's state with the next one's,
 * with no snapshot race and no second round trip.
 */
import type { ElementHandle, Frame, Page } from "playwright-core";
import {
  LOCATOR_PRECEDENCE,
  controlCharacterIn,
  escapeCssAttributeValue,
} from "../spec/locator-precedence.mts";
import type { Assertion, FrameChain, FrameRef } from "../spec/types.mts";
import {
  sensitiveMatcherSource,
  type SensitiveClassification,
} from "./sensitive-fields.mts";

/** The page-side function the bundle calls out through. */
export const CAPTURE_BINDING = "__formicCaptureEvent";

/** The bundle's own state reader, left on the page so the host can ask for the FINAL
 *  state once the human has stopped — there is no following event to carry it. Reusing
 *  the bundle's function keeps one definition of "what a reviewer can see". */
export const VISIBLE_STATE_HOOK = "__formicVisibleState";

/** Arms the listeners on the CURRENT document. `addInitScript` only reaches documents
 *  loaded afterwards, so a recording that starts on an already-open page needs this. */
export const INSTALL_HOOK = "__formicCaptureInstall";

/** Removes the listeners this bundle added. Named the same way they were added, so a
 *  stopped recording leaves nothing behind on a session the caller goes on using. */
export const TEARDOWN_HOOK = "__formicCaptureTeardown";

/** Asked by the bundle, in every document, before it arms anything.
 *
 * `addInitScript` cannot be removed and `exposeBinding` cannot be undone, so the bundle
 * runs in every document the page loads afterwards — including the ones it loads after a
 * recording has ENDED. A stopped recording used to leave a live listener set on the next
 * page the caller navigated to, calling out to a binding nobody was reading. The host
 * owns the answer to "is a recording running", so the bundle asks it. */
export const ACTIVE_BINDING = "__formicCaptureActive";

/** The page's high-water event number. Read once at teardown, it is what tells the host
 *  how much traffic is still in flight, so a stop can WAIT for delivery instead of
 *  sleeping and hoping. */
export const SEQ_HOOK = "__formicCaptureSeq";

/** The id of the document the bundle is running in. The host reads it after driving its
 *  OWN navigation, so it can recognise that navigation's announcement by identity
 *  instead of by arrival order. */
export const FRAME_HOOK = "__formicCaptureFrame";

/** The ONE classification, published so every page-side projection that needs it — the
 *  binding payload, the visible-state reader, rrweb's own mask — calls the same memoised
 *  decision rather than making a second one. */
export const CLASSIFY_HOOK = "__formicClassify";

/** Records a decision the PAGE could not have made — a spec that says a step's value
 *  comes from the environment has already settled that it is secret, whatever the field
 *  looks like. Writes into the same write-once memo table `CLASSIFY_HOOK` reads. */
export const CLASSIFY_AS_HOOK = "__formicClassifyAs";

/** Re-exported so a caller reading a recording needs one import, not two. */
export {
  SENSITIVE_TABLE,
  secretPlaceholder,
  classifySensitiveField,
  sensitiveDescriptorOf,
  type SensitiveCategory,
  type SensitiveClassification,
  type SensitiveFieldDescriptor,
} from "./sensitive-fields.mts";

export type CapturedEventKind =
  "click" | "input" | "change" | "submit" | "navigation";

/** Which kind of control the event's target is. Decided page-side, where the element
 *  is, because the replay action a step needs follows from the element's TYPE and
 *  nothing else: a `<select>` cannot be filled and a checkbox cannot be typed into. */
export type ControlKind = "text" | "editable" | "select" | "checkable";

/** Locator facts read off the event's target. Field names match `Assertion`'s on
 *  purpose: a derivation is a projection of these onto that shape, never a translation. */
export interface TargetFacts {
  testId?: string;
  /** A STABLE unique selector — an id, and nothing else. Arbitrary `data-*` attributes
   *  and positional paths are `cssFallback`, which ranks below role and text. */
  selector?: string;
  role?: string;
  name?: string;
  text?: string;
  /** The last resort: a unique `data-*` attribute or a positional CSS path. Never wins
   *  precedence over role or text — it is the address of a POSITION, not of a thing. */
  cssFallback?: string;
  /** The classification the target's content carries — see `VisibleNode.classification`. */
  classification?: SensitiveClassification;
  /** Set when `text` is a prefix the page had to cut — see `VisibleNode.truncated`. */
  truncated?: boolean;
  /** A temporary attribute value the page stamped on the target so the HOST can ask
   *  Playwright about it. Consumed and removed by `proveTargetFacts`; never emitted. */
  probe?: string;
}

/** One element a reviewer can see, as the page reported it. */
export interface VisibleNode {
  testId?: string;
  text: string;
  /** The classification this node's own content carries — its own, or the first
   *  classified thing inside it. No projection may quote what it names. */
  classification?: SensitiveClassification;
  /** Set when `text` is a prefix the page had to cut. An exact match on a prefix is an
   *  assertion that cannot pass on the very page it was recorded from. */
  truncated?: boolean;
  /** Whether this element is a heading whose accessible name is its own text AND names
   *  exactly one heading in the document — both proven where the document is. */
  heading?: boolean;
  /** Whether `text` names exactly one element, and may therefore be a locator rather
   *  than only something to display. */
  textLocator?: boolean;
  /** Set when a heading's name is too long to be claimed. The host names the refusal;
   *  a silently shortened name would match nothing at all. */
  nameTooLong?: boolean;
}

/** What the page showed at the moment an event fired — the material an assertion
 *  proposal is derived from. */
export interface VisibleState {
  url: string;
  nodes: VisibleNode[];
  /**
   * Whether the element the PREVIOUS action touched is still attached and visible.
   *
   * Absent when no action has been reported in this document yet — including every
   * state a fresh document produces, where the old element is gone by construction.
   */
  actedStillVisible?: boolean;
}

export interface CapturedEvent {
  kind: CapturedEventKind;
  /** The PAGE's `Date.now()`, not the host's. See the file header. */
  timestamp: number;
  url: string;
  /** The document this event came from, and a per-document monotonic counter. Together
   *  they name the event, which is what lets a consequence point back at its cause and
   *  what lets the stop barrier know it has drained everything. */
  frameId: string;
  seq: number;
  /**
   * The nested browsing context this event happened in, outermost first — ABSENT for the
   * top-level page, which is where nearly every event happens.
   *
   * Built HOST-side, from Playwright's `source.frame`, because a document cannot see the
   * element that owns it in its parent. `frameId` above is the page-side document
   * identity used to sequence traffic; this is the address a spec can carry.
   */
  frame?: FrameChain;
  /**
   * Whether this event came from a NESTED document rather than the top-level page.
   *
   * Set even when no chain could be derived, because the host still has to account for
   * the event: the stop barrier counts what each document sent, and an event withheld
   * from the pump is one the barrier waits for until the deadline — an untouched iframe
   * announcing its own load was enough to make every recording take the full five
   * seconds to stop.
   */
  nested?: boolean;
  /** Present on every ACTION. A navigation that names it is that action's consequence. */
  actionId?: string;
  /** `navigation` (same-document only): the action whose activation was in flight when
   *  the route changed. Absent means nothing caused it — the human moved. */
  causeActionId?: string;
  /** `navigation`: whether the route changed inside this document rather than by loading
   *  a new one. */
  sameDocument?: boolean;
  /** `navigation` of a NEW document: whether the document that came before it INITIATED
   *  the navigation. The browser's own causal signal — a link click, a form submission
   *  or a scripted assignment carries a referrer however long it took to arrive, and an
   *  address typed by hand carries none. */
  referred?: boolean;
  state: VisibleState;
  target?: TargetFacts;
  value?: string;
  tagName?: string;
  control?: ControlKind;
  /**
   * The ONE classification record for the event's target, when it holds personal or
   * secret data — the only sensitivity input any host-side projection has.
   *
   * It used to be COPIED into three parallel fields, one per consumer, and the record
   * itself was read by nobody: the policy-boundary class re-instantiated inside the fix
   * for it. A withheld value is `classification` present and `value` ABSENT; with
   * `--include-secrets` both are present, which is the whole of the difference.
   */
  classification?: SensitiveClassification;
  /** `submit` only: whether the form named a submitter. A submitted form WITH one was
   *  reached by activating that control, so a click for it was already recorded; one
   *  WITHOUT is a keyboard submission, which is a step of its own. */
  submitter?: boolean;
}

export interface CaptureOptions {
  /** Record password values verbatim. Off by default; `record --include-secrets` is the
   *  only thing that turns it on, and it is the human saying so out loud. */
  includeSecrets?: boolean;
  /** Told, once per frame, about an event the recording will not represent. Refusing
   *  silently is the one outcome that is never acceptable: the human would read a spec
   *  that is missing what they did and have no way to know it. */
  onRefusal?: (warning: string) => void;
}

/** Refuses a locator value that cannot be carried losslessly, naming the character. */
function refuseControlCharacters(field: string, value: string): void {
  const control = controlCharacterIn(value);
  if (control !== null) {
    throw new Error(
      `the ${field} ${JSON.stringify(value)} contains the control character ${control}, which no locator form can carry`,
    );
  }
}

/**
 * The locator the spec will carry for this target: the first field of
 * `LOCATOR_PRECEDENCE` the page could report, as a STRUCTURED assertion. `role` keeps
 * its accessible name and asks for a whole-string match — a substring `name` would match
 * a second control the moment one appears, and a recording must not write an assertion
 * looser than what was seen.
 *
 * `cssFallback` is consulted only when nothing else was reportable, which is what makes
 * the effective order `testId > id > role/text > positional CSS` rather than letting a
 * structural path outrank the accessible name a reviewer would recognise.
 */
export function preferredLocator(facts: TargetFacts): Assertion {
  for (const field of LOCATOR_PRECEDENCE) {
    const value = facts[field];
    if (value === undefined || value === "") continue;
    refuseControlCharacters(field, value);
    if (field === "role") {
      if (facts.name === undefined || facts.name === "") return { role: value };
      refuseControlCharacters("name", facts.name);
      return { role: value, name: facts.name, exact: true };
    }
    return { [field]: value } as Assertion;
  }
  if (facts.cssFallback !== undefined && facts.cssFallback !== "") {
    refuseControlCharacters("selector", facts.cssFallback);
    return { selector: facts.cssFallback };
  }
  throw new Error(
    "no locator could be derived — the target reported no testId, selector, role or text",
  );
}

/**
 * The step's `target`, which the grammar states as a string and the runner hands to
 * `page.click`/`page.fill`.
 *
 * CSS ONLY. Playwright's selector engines (`role=`, `text=`) take quoted arguments, and
 * a value pasted into one is page-controlled text inside a syntax with its own escapes:
 * a test id of `x"], body, [x="` resolved `BODY` while the structured locator resolved
 * the button. So a target is either a `data-testid` attribute selector with its value
 * escaped, a stable id, or the positional path — three forms whose escaping is total —
 * and the ROLE and TEXT observations ride in the step's assertion, structured, where
 * Playwright applies them itself.
 */
export function targetSelectorFor(facts: TargetFacts): string {
  if (facts.testId !== undefined && facts.testId !== "") {
    refuseControlCharacters("testId", facts.testId);
    return `[data-testid="${escapeCssAttributeValue(facts.testId)}"]`;
  }
  const css = facts.selector || facts.cssFallback;
  if (css !== undefined && css !== "") {
    refuseControlCharacters("selector", css);
    return css;
  }
  throw new Error(
    "no step target could be derived — the target reported no testId and no CSS selector",
  );
}

/**
 * The bundle, as source. Written against no build step and no framework so it can be
 * handed to `addInitScript` verbatim, and kept free of template interpolation so the
 * page's own `${...}` never collides with this file's.
 */
export function captureBundleSource(options: CaptureOptions = {}): string {
  return CAPTURE_BUNDLE.replace(/__BINDING__/g, CAPTURE_BINDING)
    .replace(/__STATE_HOOK__/g, VISIBLE_STATE_HOOK)
    .replace(/__INSTALL_HOOK__/g, INSTALL_HOOK)
    .replace(/__TEARDOWN_HOOK__/g, TEARDOWN_HOOK)
    .replace(/__CLASSIFY_HOOK__/g, CLASSIFY_HOOK)
    .replace(/__SEQ_HOOK__/g, SEQ_HOOK)
    .replace(/__FRAME_HOOK__/g, FRAME_HOOK)
    .replace(/__ACTIVE_BINDING__/g, ACTIVE_BINDING)
    .replace(/__INCLUDE_SECRETS__/g, options.includeSecrets ? "true" : "false")
    .replace(/__CLASSIFIER_BUNDLE__/g, () => classifierBundleSource())
    .replace(
      /__ESCAPE_ATTRIBUTE__/g,
      () =>
        `var escapeCssAttributeValue = ${escapeCssAttributeValue.toString()};`,
    );
}

/**
 * The classification, as page-injectable source — the ONE decision, wherever a page is
 * being watched.
 *
 * It used to live inside the capture bundle, which only a RECORDING installs. A replay
 * installs rrweb and nothing else, so the evidence bundle a replay produces — the
 * artifact that gets attached to a pull request — fell back to rrweb's own default and
 * masked passwords alone, while every other category the recorder is careful about went
 * in verbatim. Evidence is not a lesser plane than a spec.
 *
 * So it is its own bundle now, injected by whoever installs the replay recorder
 * (driver/rrweb-recorder.mts) and again by a recording. It installs ONCE per document —
 * whichever arrives first owns the memo table, and the second is a no-op — so a session
 * that is both replaying and recording still has exactly one answer per element.
 */
export function classifierBundleSource(): string {
  return CLASSIFIER_BUNDLE.replace(/__CLASSIFY_HOOK__/g, CLASSIFY_HOOK)
    .replace(/__CLASSIFY_AS_HOOK__/g, CLASSIFY_AS_HOOK)
    .replace(/__SENSITIVE_MATCHER__/g, () => sensitiveMatcherSource());
}

const CLASSIFIER_BUNDLE = `
(() => {
  if (window.__CLASSIFY_HOOK__) return;
  __SENSITIVE_MATCHER__

  // The ONE classification, decided ONCE per element and memoised WRITE-ONCE.
  //
  // Sensitivity used to be re-decided on every event from whatever the element looked
  // like at that moment, and an element's metadata is not stable: a reveal toggle flips
  // its type from password to text, a framework rewrites its name on rerender. The same
  // field's secret was then withheld on one event and written down on the next. A
  // decision is not a poll — the first answer is the answer, in both directions.
  var classifications = new WeakMap();
  function classify(element) {
    if (!element || element.nodeType !== 1) return null;
    if (classifications.has(element)) return classifications.get(element);
    var decided = classifySensitiveField(
      sensitiveDescriptorOf(element),
      SENSITIVE_TABLE
    );
    classifications.set(element, decided);
    return decided;
  }
  // A value lives in a CONTROL, so the question "is this node's content sensitive?" is
  // answered at the nearest control at or above it — the input itself, or the element the
  // author marked contenteditable. Walking every ancestor instead would classify the
  // wrapping <label> whose text is "Card number" and redact the page's own labels, which
  // makes a replay unreadable without protecting anything.
  function valueBearingHost(node) {
    var element = node;
    if (element && element.nodeType !== 1) element = element.parentElement;
    while (element && element.nodeType === 1) {
      var tag = element.tagName;
      var editable = element.getAttribute("contenteditable");
      if (
        tag === "INPUT" ||
        tag === "TEXTAREA" ||
        tag === "SELECT" ||
        (editable !== null && editable !== "false")
      ) {
        return element;
      }
      element = element.parentElement;
    }
    return null;
  }
  // The memo that owns THIS node — its own document's, which is not always this one.
  //
  // rrweb records the top-level document and observes same-origin child documents
  // through it, so the masking callback that sees a child frame's input is the PARENT's.
  // A decision written into the child's memo — which is where markReferencedTarget
  // writes it, because it evaluates in the element's own realm — was invisible to the
  // callback that actually masks, and a referenced value typed into an iframe went into
  // the replay stream in the clear. One memo per document, and every hook resolves to
  // the node's own before answering.
  function memoFor(node) {
    try {
      var owner = node && node.ownerDocument;
      var view = owner && owner.defaultView;
      if (view && view !== window) return view;
    } catch (crossOrigin) {
      // A node from a document this realm cannot reach is not a node this realm was
      // given; answering locally is the only thing left, and it is the safe one.
    }
    return null;
  }
  // The one page-side answer every projection asks for, the replay recorder included.
  window.__CLASSIFY_HOOK__ = function (node) {
    var owner = memoFor(node);
    if (owner && typeof owner.__CLASSIFY_HOOK__ === "function") {
      return owner.__CLASSIFY_HOOK__(node);
    }
    var host = valueBearingHost(node);
    return host ? classify(host) : null;
  };
  // A decision the page could not have made, written into the SAME memo table so every
  // projection sees one record per element. Write-once like every other entry: a field
  // the page already classified keeps its own category and its own provenance, which is
  // the more specific answer and the one a reviewer can account for.
  window.__CLASSIFY_AS_HOOK__ = function (node, category) {
    var owner = memoFor(node);
    if (owner && typeof owner.__CLASSIFY_AS_HOOK__ === "function") {
      return owner.__CLASSIFY_AS_HOOK__(node, category);
    }
    var host = valueBearingHost(node);
    if (!host) return false;
    if (classifications.get(host)) return true;
    classifications.set(host, {
      category: category,
      source: "reference",
      evidence: "valueFrom"
    });
    return true;
  };
})();
`;

/**
 * The page's own view of what is visible right now. Only the LAST step needs this — every
 * earlier step's end state arrives on the next event, captured before the page could
 * change again. A page with no bundle on it (never navigated, already closed) reports
 * nothing rather than failing a recording that is otherwise complete.
 */
export async function readVisibleState(
  page: Page | Frame,
): Promise<VisibleState> {
  const readHook = (name: string): unknown =>
    (globalThis as unknown as Record<string, () => unknown>)[name]();
  try {
    return (await page.evaluate(readHook, VISIBLE_STATE_HOOK)) as VisibleState;
  } catch {
    return { url: page.url(), nodes: [] };
  }
}

/** What a recording holds while it runs, and gives back when it stops. */
export interface CaptureHandle {
  /** Take the listeners off the page, so no NEW event can be produced. Says nothing
   *  about events already in flight — those still have somewhere to land, which is what
   *  lets the host drain them instead of sleeping and hoping. Idempotent. */
  stopListening(): Promise<void>;
  /** Wait for every payload that has already CROSSED the binding to reach the sink.
   *  Naming a child frame takes a round trip, so an event can be received and not yet
   *  delivered; the barrier reads its high-water marks after this, never before. */
  settleDelivery(): Promise<void>;
  /** Stop delivering entirely. Anything still crossing the binding after this is
   *  dropped, so it is called once the drain is done. Idempotent. */
  detach(): Promise<void>;
}

/** `exposeBinding` cannot be undone and refuses a second registration of the same name,
 *  so the binding is installed once per page and the SINK is what a recording swaps.
 *  Without this a session could be recorded only once in its life. */
const activeSinks = new WeakMap<Page, (event: CapturedEvent) => void>();
const activeRefusals = new WeakMap<Page, (warning: string) => void>();
const boundPages = new WeakSet<Page>();

/**
 * Everything ONE recording accumulates that the permanent binding has to read.
 *
 * The binding is registered once per page and can never be replaced, so anything it
 * closes over belongs to the FIRST recording forever. The delivery chain was the one
 * that bit: a second recording awaited its own fresh promise while the binding kept
 * extending the first recording's, so `settleDelivery` returned immediately and the
 * stop barrier read its marks with the human's last events still unqueued — a click
 * missing from the spec, with no warning, only on the second recording of a session.
 * The cache, the document paths and the refusal set were stale for the same reason.
 *
 * So the binding holds no state of its own. It looks the current recording up here,
 * every time, and a new recording is a new object.
 */
interface RecordingState {
  /** Serialises delivery: naming a child frame is a round trip, and an await on the
   *  event path would let a later event overtake an earlier one. */
  delivering: Promise<void>;
  chains: Map<string, FrameChain | { refuse: string }>;
  /** The path to each nested document, as it stood when that document was first seen. */
  documentPaths: Map<string, FrameLinkFacts[]>;
  refusedFrames: Set<string>;
  /** False from the moment the human stops. A document that loads during the drain asks
   *  before arming, and must be told no — otherwise a page that navigates while the
   *  recording is winding down starts listening into a sink that is still open. */
  listening: boolean;
}

const recordings = new WeakMap<Page, RecordingState>();

/** Wire the bundle to `page`. Call BEFORE the first navigation — the init script runs
 *  at the start of every document the page loads afterwards, which is also what makes a
 *  navigation self-announcing. The current document is armed explicitly, because
 *  `addInitScript` never reaches a document that is already open. */
export async function attachCaptureListeners(
  page: Page,
  onEvent: (event: CapturedEvent) => void,
  options: CaptureOptions = {},
): Promise<CaptureHandle> {
  activeSinks.set(page, onEvent);
  activeRefusals.set(page, options.onRefusal ?? (() => {}));
  // A NEW object per recording, looked up by the binding rather than closed over — see
  // RecordingState. Every payload joins one serialised delivery queue, frame or no frame,
  // so ordering is a property of the path rather than of which events needed a round trip.
  const state: RecordingState = {
    delivering: Promise.resolve(),
    chains: new Map(),
    documentPaths: new Map(),
    refusedFrames: new Set(),
    listening: true,
  };
  recordings.set(page, state);
  if (!boundPages.has(page)) {
    await page.exposeBinding(
      ACTIVE_BINDING,
      () => recordings.get(page)?.listening === true && activeSinks.has(page),
    );
    await page.exposeBinding(CAPTURE_BINDING, (source, json: string) => {
      // `exposeBinding` reaches EVERY frame, and so does the init script, so a child
      // document reports through it too. Playwright's own `source.frame` is the
      // authority on WHERE an event came from, and the grammar can now carry that: the
      // host names the chain from the page down to the frame and the event carries it,
      // so the step is addressed in the document it actually happened in. A frame that
      // cannot be named at all is still refused BY NAME — silently folding it into the
      // top-level page is the one outcome that was never acceptable.
      const frame = source.frame;
      // PARSED AND SNAPSHOTTED HERE, synchronously, before the event joins the delivery
      // queue. Everything the naming of a nested document depends on — its own name, its
      // URL, how many siblings share either — is read off a LIVE frame, and the walk
      // below runs after an await. A click queued while the frame was on /before was
      // named with /after's URL, so the step addressed a document the human never
      // touched. The facts are captured when the document is first seen; the walk
      // consults the capture.
      const current = recordings.get(page);
      if (current === undefined) return;
      const event = JSON.parse(json) as CapturedEvent;
      const nested = frame !== page.mainFrame();
      if (nested && !current.documentPaths.has(event.frameId)) {
        current.documentPaths.set(event.frameId, framePathFacts(page, frame));
      }
      current.delivering = current.delivering.then(async () => {
        if (nested) {
          event.nested = true;
          // A NAVIGATION reported by a child document is that frame loading, not
          // something the human did — the grammar's `goto` navigates the page, and the
          // validator refuses a frame on one. It is DELIVERED all the same, and the host
          // declines to make a step of it: withholding it here left its sequence number
          // uncounted, and the stop barrier then waited the full drain deadline for
          // traffic that had already been thrown away. An iframe nobody touched was
          // enough to make every recording take five seconds to stop.
          if (event.kind !== "navigation") {
            const chain = await frameChainFor(
              event.frameId,
              current.documentPaths.get(event.frameId) ?? [],
              current.chains,
            );
            if ("refuse" in chain) {
              const where = frame.url();
              if (!current.refusedFrames.has(where)) {
                current.refusedFrames.add(where);
                activeRefusals.get(page)?.(
                  `an interaction in a nested frame (${where}) was not recorded — ${chain.refuse}`,
                );
              }
              // Delivered without a chain, for the count. The host makes no step of an
              // event it cannot address, and the refusal above is what the human reads.
              activeSinks.get(page)?.(event);
              return;
            }
            event.frame = chain;
          }
        }
        activeSinks.get(page)?.(event);
      });
    });
    boundPages.add(page);
  }
  // On the CONTEXT, not the page. `page.addInitScript` does not reach child frames
  // (measured: the binding is present in a child, the bundle is not), so an interaction
  // inside an iframe produced nothing at all — and a refusal that never fires is
  // indistinguishable from a recording that silently drops what the human did. Installed
  // here the child frame runs the bundle, reports, and IS refused by name.
  await page.context().addInitScript({ content: captureBundleSource(options) });
  // …and explicitly into every CHILD frame as it navigates. An init script does not
  // reach a subframe of a page that already existed when it was added (measured: the
  // binding and the context-level classifier are both present in a child, the capture
  // bundle is not), so an interaction inside an iframe produced NOTHING — and a refusal
  // that never fires is indistinguishable from a recording silently dropping what the
  // human did. Armed here, the child reports and is refused by name.
  const armFrame = (frame: Frame) => {
    if (frame === page.mainFrame()) return;
    void frame.evaluate(captureBundleSource(options)).catch(() => {
      // A frame that navigated away or is cross-origin cannot be armed. It is then
      // unseen rather than refused — the same outcome the grammar forces either way.
    });
  };
  page.on("framenavigated", armFrame);
  for (const frame of page.frames()) armFrame(frame);
  await callPageHook(page, INSTALL_HOOK);
  let listening = true;
  let detached = false;
  const stopListening = async () => {
    if (!listening) return;
    listening = false;
    // Told FIRST, and told through the binding every document asks before arming: a page
    // or a frame that loads during the drain must not start listening into a sink that
    // is deliberately still open. Teardown below reaches the documents that are already
    // here; this reaches the ones that are not here yet.
    state.listening = false;
    page.off("framenavigated", armFrame);
    // EVERY document, not just the top-level one. The bundle arms listeners in each
    // frame it reaches, so tearing down the main document alone left a child frame still
    // listening and still reporting — and the sink stays until the drain is done, so
    // whatever the human did to that iframe after pressing stop went into the spec.
    await Promise.all(
      page.frames().map((frame) => callPageHook(frame, TEARDOWN_HOOK)),
    );
  };
  return {
    stopListening,
    async settleDelivery() {
      // THIS recording's queue, read from the shared state — not a promise captured when
      // the handle was made, and not the first recording's. Twice: the first await lets
      // whatever was queued run, and anything it queued in turn joins before the second.
      await recordings.get(page)?.delivering;
      await recordings.get(page)?.delivering;
    },
    async detach() {
      if (detached) return;
      detached = true;
      await stopListening();
      activeSinks.delete(page);
      activeRefusals.delete(page);
    },
  };
}

/** Best-effort: a page that has navigated away, closed, or never ran the bundle simply
 *  has no hook to call, and neither arming nor tearing down may fail a recording. */
async function callPageHook(page: Page | Frame, hook: string): Promise<void> {
  try {
    await page.evaluate((name: string) => {
      const fn = (globalThis as unknown as Record<string, unknown>)[name];
      if (typeof fn === "function") (fn as () => void)();
    }, hook);
  } catch {
    // Nothing to arm or tear down here.
  }
}

const CAPTURE_BUNDLE = `
(() => {
  if (location.href === "about:blank") return;
  // The host adds this script once per recording, so a session recorded twice loads it
  // twice into the same document. Each copy is its own closure, and a second copy's
  // listeners are different function objects that no teardown can remove — the same
  // click would arrive twice. Only the first copy installs; a later one hands over its
  // secrets decision (the most recent caller's) and re-arms what is already there.
  window.__formicCaptureSecrets = __INCLUDE_SECRETS__;
  if (window.__formicCaptureBundle) {
    if (typeof window.__INSTALL_HOOK__ === "function") window.__INSTALL_HOOK__();
    return;
  }
  window.__formicCaptureBundle = true;
  var MAX_TEXT = 120;
  var MAX_NODES = 40;
  // What DISPLAY text is capped at is a readability budget. What an EXACT name is capped
  // at is a correctness one, and they are not the same number: a name sliced to the
  // display budget and then matched whole-string can never pass.
  // Not a truncation point — a REFUSAL point. Beyond this a heading's name stops being
  // something a human would recognise in a spec, so no name is claimed at all and the
  // host is told why.
  var MAX_EXACT_NAME = 2000;
  // This document's identity, and a monotonic counter over everything it sends.
  var frameId = "f" + Math.random().toString(36).slice(2, 10);
  var seq = 0;
  // Only these CREATE an activation a navigation may be owned by.
  var ACTIVATING = { click: true, change: true, submit: true };
  var ACTIVATION_KEY = "__formicActivation";
  var pendingActivation = null;
  __ESCAPE_ATTRIBUTE__
  __CLASSIFIER_BUNDLE__

  function send(payload) {
    var out = window.__BINDING__;
    if (typeof out === "function") out(JSON.stringify(payload));
  }
  // Proof, not hope: a candidate is emitted only when it resolves to exactly ONE element
  // and that element is the one the event was about. Everything the page reports about
  // an address has been through this.
  function resolves(selector, element) {
    var found;
    try {
      found = document.querySelectorAll(selector);
    } catch (invalid) {
      return false;
    }
    return found.length === 1 && found[0] === element;
  }
  function textOf(element) {
    return (element.textContent || "").replace(/\\s+/g, " ").trim().slice(0, MAX_TEXT);
  }
  // Text is a channel too. A contenteditable's typed content never went through the
  // value channel at all: it rode out as the target's own text fact and as a node in the
  // visible state, where nothing was withholding anything, and then became an assertion
  // quoting the secret back. One classification, every projection — including this one.
  // Text is a channel too, and it AGGREGATES. Masking only the node and its ancestors
  // left every CONTAINER reporting its own textContent, so a sensitive editable nested
  // inside a heading or a [data-testid] panel walked its secret out through the box
  // around it — the same leak one level up. The subtree is what gets masked: every
  // classified descendant is replaced by its placeholder, in place, and what is left is
  // the page's own words.
  function safeTextOf(element, whole) {
    if (classifyValueOf(element)) return "";
    var pieces = [];
    // DISPLAY text is capped for readability and says so with its truncated flag. An
    // EXACT name
    // is never cut: a prefix of a name is not a shorter name, it is a different one that
    // matches nothing. A name too long to be sensible is refused instead, out loud.
    var budget = { left: whole ? (MAX_EXACT_NAME + 1) * 4 : MAX_TEXT * 4 };
    collectText(element, pieces, budget);
    var text = pieces.join("").replace(/\\s+/g, " ").trim();
    return whole ? text : text.slice(0, MAX_TEXT);
  }
  function collectText(node, pieces, budget) {
    if (budget.left <= 0) return;
    for (var i = 0; i < node.childNodes.length; i++) {
      var child = node.childNodes[i];
      if (child.nodeType === 3) {
        // Collapsed BEFORE it is charged to the budget. Counting raw characters let a
        // run of whitespace exhaust the traversal before it reached the rest of the
        // heading, and the name came back a silent prefix of itself.
        var normalised = child.data.replace(/\\s+/g, " ");
        pieces.push(normalised);
        budget.left -= normalised.length;
      } else if (child.nodeType === 1) {
        var found = classifyValueOf(child);
        if (found) {
          pieces.push(" <secret:" + found.category + "> ");
        } else {
          collectText(child, pieces, budget);
        }
      }
      if (budget.left <= 0) return;
    }
  }
  // The sensitivity a node's TEXT carries: its own when it is a classified control, and
  // otherwise the first classified thing inside it — a container holding a secret is not
  // a safe thing to quote in an assertion just because the container is not a field.
  // What an ENGINE would call this heading, to the extent this bundle will claim to
  // know: an explicit aria-label, else the text an aria-labelledby points at, else its
  // own text. Not a full accessible-name computation — a deliberately SHALLOW one, used
  // only to detect a COLLISION and refuse. Erring toward refusal is safe (the locator
  // precedence falls back); erring toward a claim is what shipped a name matching two
  // elements.
  function headingNameTooLong(element) {
    var isHeading =
      /^H[1-6]$/.test(element.tagName) ||
      element.getAttribute("role") === "heading";
    if (!isHeading) return false;
    if (element.getAttribute("aria-label")) return false;
    if (element.getAttribute("aria-labelledby")) return false;
    return safeTextOf(element, true).length > MAX_EXACT_NAME;
  }
  function claimedNameOf(element) {
    var label = element.getAttribute("aria-label");
    if (label) return label.replace(/\\s+/g, " ").trim();
    var labelledBy = element.getAttribute("aria-labelledby");
    if (labelledBy) {
      var pieces = [];
      var ids = labelledBy.split(/\s+/);
      for (var i = 0; i < ids.length; i++) {
        var referenced = document.getElementById(ids[i]);
        if (referenced) pieces.push(safeTextOf(referenced));
      }
      return pieces.join(" ").replace(/\\s+/g, " ").trim();
    }
    return safeTextOf(element, true);
  }
  function isProvenHeading(element) {
    var isHeading =
      /^H[1-6]$/.test(element.tagName) ||
      element.getAttribute("role") === "heading";
    if (!isHeading) return false;
    // Our own name must BE our own text, or the name we report is not the name an
    // engine would match on.
    if (element.getAttribute("aria-label")) return false;
    if (element.getAttribute("aria-labelledby")) return false;
    var mine = safeTextOf(element, true);
    if (mine === "" || mine.length > MAX_EXACT_NAME) return false;
    // Every OTHER heading is compared by the name an engine would give IT — a sibling
    // whose aria-label reads the same collides, however different its text looks.
    var headings = document.querySelectorAll(
      "h1, h2, h3, h4, h5, h6, [role=heading]"
    );
    var matches = 0;
    for (var i = 0; i < headings.length; i++) {
      if (claimedNameOf(headings[i]) === mine) matches += 1;
    }
    return matches === 1;
  }
  // Whether this text resolves to exactly one element, in the sense a text locator
  // resolves: the innermost elements whose own text is the whole string.
  function isUniqueText(element, text) {
    if (!text) return false;
    var all = document.querySelectorAll("body *");
    var matches = 0;
    for (var i = 0; i < all.length; i++) {
      var candidate = all[i];
      if (safeTextOf(candidate) !== text) continue;
      var inner = candidate.querySelectorAll("*");
      var deeper = false;
      for (var j = 0; j < inner.length; j++) {
        if (safeTextOf(inner[j]) === text) deeper = true;
      }
      if (!deeper) matches += 1;
    }
    void element;
    return matches === 1;
  }
  function subtreeClassification(element) {
    var own = classifyValueOf(element);
    if (own) return own;
    var found = element.querySelectorAll("input, textarea, select, [contenteditable]");
    for (var i = 0; i < found.length; i++) {
      var inner = classifyValueOf(found[i]);
      if (inner) return inner;
    }
    return null;
  }
  // Whether the page had to CUT the text it is reporting. An exact match on a prefix
  // cannot pass on the page it was recorded from, so the fact travels with the text.
  function isTruncated(element) {
    if (classifyValueOf(element)) return false;
    return (element.textContent || "").replace(/\\s+/g, " ").trim().length > MAX_TEXT;
  }
  function isVisible(element) {
    var rect = element.getBoundingClientRect();
    return rect.width > 0 && rect.height > 0;
  }
  function inputType(element) {
    return (element.getAttribute("type") || "text").toLowerCase();
  }
  function attributeSelector(name, value) {
    return "[" + name + '="' + escapeCssAttributeValue(value) + '"]';
  }
  function idSelector(element) {
    if (!element.id) return "";
    var byId = "#" + (window.CSS && CSS.escape ? CSS.escape(element.id) : element.id);
    return resolves(byId, element) ? byId : "";
  }
  function uniqueDataSelector(element) {
    for (var i = 0; i < element.attributes.length; i++) {
      var attribute = element.attributes[i];
      if (attribute.name.indexOf("data-") !== 0) continue;
      if (attribute.name === "data-testid" || !attribute.value) continue;
      if (attribute.name === PROBE_ATTRIBUTE) continue;
      var candidate = attributeSelector(attribute.name, attribute.value);
      if (resolves(candidate, element)) return candidate;
    }
    return "";
  }
  // The address the HOST will need if it wants to ask Playwright about this element —
  // a temporary attribute, unique by construction, removed as soon as the host has read
  // what it needed. It exists because the accessible name is Playwright's answer to
  // give, not the page's: a second HTML-to-ARIA implementation here called every input
  // a textbox and every select a combobox, so the role and name it reported did not
  // resolve in the engine the replay uses.
  var PROBE_ATTRIBUTE = "data-formic-probe";
  var probeCount = 0;
  function probeFor(element) {
    probeCount += 1;
    var probe = "probe-" + probeCount;
    element.setAttribute(PROBE_ATTRIBUTE, probe);
    return probe;
  }
  function factsFor(element) {
    var facts = {};
    var testId = element.getAttribute("data-testid");
    if (testId && resolves(attributeSelector("data-testid", testId), element)) {
      facts.testId = testId;
    }
    var byId = idSelector(element);
    if (byId) facts.selector = byId;
    var byData = uniqueDataSelector(element);
    if (byData) facts.cssFallback = byData;
    var classified = subtreeClassification(element);
    if (classified) facts.classification = classified;
    var text = safeTextOf(element);
    if (text) {
      facts.text = text;
      if (isTruncated(element)) facts.truncated = true;
    }
    // No positional path. It was the address of a POSITION — invalid at body, impossible
    // at html, unrelated to its host under a shadow root — and it made every target look
    // derivable while some of them resolved nothing. A target the page cannot prove is
    // handed to the host as a probe, and if the host cannot prove it either the step is
    // refused rather than written down wrong.
    facts.probe = probeFor(element);
    return facts;
  }
  function controlKind(element) {
    var tag = element.tagName;
    if (tag === "SELECT") return "select";
    if (tag === "TEXTAREA") return "text";
    if (tag === "INPUT") {
      var type = inputType(element);
      if (type === "checkbox" || type === "radio") return "checkable";
      if (type === "submit" || type === "button" || type === "reset") return "";
      if (type === "file") return "";
      return "text";
    }
    if (element.isContentEditable) return "editable";
    return "";
  }
  // The ONE classification lives in CLASSIFIER_BUNDLE (injected above), which publishes
  // it as a page hook and installs itself only once per document. Everything here reads
  // that hook rather than deciding again — including a document where the replay
  // recorder installed the classifier first and the recording arrived afterwards.
  var classifyValueOf = window.__CLASSIFY_HOOK__;
  window.__SEQ_HOOK__ = function () {
    return seq;
  };
  window.__FRAME_HOOK__ = function () {
    return frameId;
  };
  function valueOf(element, kind) {
    if (kind === "editable") return (element.textContent || "");
    return element.value === undefined || element.value === null
      ? ""
      : String(element.value);
  }
  // The element the last reported action touched, so the NEXT state can say what became
  // of it. Held page-side because only the page can answer "is it still there" without
  // racing the hand: by the time the host could ask, the human has moved on.
  var lastActedElement = null;
  function visibleState() {
    var nodes = [];
    var found = document.querySelectorAll("[data-testid], h1, h2, h3");
    for (var i = 0; i < found.length && nodes.length < MAX_NODES; i++) {
      var element = found[i];
      if (!isVisible(element)) continue;
      var node = { text: safeTextOf(element) };
      var classifiedNode = subtreeClassification(element);
      if (classifiedNode) node.classification = classifiedNode;
      else if (isTruncated(element)) node.truncated = true;
      var testId = element.getAttribute("data-testid");
      if (testId && resolves(attributeSelector("data-testid", testId), element)) {
        node.testId = testId;
      }
      // A tag read, not an accessibility model. Whether an element is a heading is the
      // one structural fact a proposal needs from the page; the ROLE and the NAME that
      // reach a spec are Playwright's answer, proven by the host before either is
      // offered to the human.
      // A heading's role and NAME become an exact locator, so both are proven here —
      // where the document is, at the instant the state is observed. A proof made
      // host-side afterwards is a proof about a page the human has already left.
      //
      // Two conditions, and the claim is dropped rather than guessed if either fails:
      // the page must not have OVERRIDDEN the accessible name (an aria-label that
      // disagrees with the visible words would make the name we report the wrong one,
      // and answering that ourselves is the second ARIA model this bundle refuses to
      // grow), and the text must name exactly ONE heading in the document.
      if (isProvenHeading(element)) {
        node.heading = true;
        // Reported WHOLE: this text becomes an exact accessible name.
        node.text = safeTextOf(element, true);
        delete node.truncated;
      } else if (headingNameTooLong(element)) {
        node.nameTooLong = true;
      }
      // Text is the last locator the precedence offers, and it is offered only when it
      // NAMES one element. Two paragraphs that read the same are not an address.
      if (isUniqueText(element, node.text)) node.textLocator = true;
      nodes.push(node);
    }
    var state = { url: location.href, nodes: nodes };
    // What became of the element the previous action touched — attached to this document
    // AND visible, or gone. The page is the only thing that can say so truthfully at the
    // moment the state is observed.
    if (lastActedElement !== null) {
      state.actedStillVisible =
        lastActedElement.isConnected === true && isVisible(lastActedElement);
    }
    return state;
  }
  function emit(kind, element, extra) {
    seq += 1;
    var payload = {
      kind: kind,
      timestamp: Date.now(),
      url: location.href,
      frameId: frameId,
      seq: seq,
      // Computed BEFORE this action's element is remembered, so the state an event
      // carries reports on the PREVIOUS action — which is exactly the after-state of the
      // step this event closes.
      state: visibleState()
    };
    if (kind !== "navigation") {
      if (element) lastActedElement = element;
      // Every event names itself, and the name is what a consequence points back at.
      payload.actionId =
        extra && extra.reuseActionId ? extra.reuseActionId : frameId + ":" + seq;
      // An ACTIVATION is what a navigation can be owned by, and only a user ACTIVATING
      // something creates one: a click, a change, a submission. Typing does not — a
      // route that arrives long after some keystrokes was caused by something the
      // recorder cannot see, and blaming the last field touched is a guess.
      //
      // It is never cleared by a timer. A timer measures how long the page took, which
      // is the thing that was wrong before: a router that pushes a route a second later
      // is still that click's router. It is replaced by the NEXT activation and
      // consumed by the navigation that claims it, and nothing else ends it.
      // Any trusted user input ENDS the activation that was open: the human has moved
      // on, and a route that arrives after they did was not caused by what they did
      // before. Typing only ends one; it never starts one, because a route arriving long
      // after some keystrokes was caused by something the recorder cannot see.
      pendingActivation = null;
      if (ACTIVATING[kind]) {
        pendingActivation = payload.actionId;
        rememberActivation(payload.actionId, isNavigational(element, kind));
      } else {
        forgetActivation();
      }
    }
    if (element && element.nodeType === 1) {
      payload.target = factsFor(element);
      payload.tagName = element.tagName.toLowerCase();
    }
    if (extra) {
      for (var key in extra) {
        if (Object.prototype.hasOwnProperty.call(extra, key)) payload[key] = extra[key];
      }
    }
    send(payload);
  }
  // Carried where the NEXT document can read it. A document load destroys every
  // variable in this bundle, so an id kept only in memory cannot survive the navigation
  // it is supposed to explain. sessionStorage is per-origin and per-tab, which is
  // exactly the scope of "the page I just came from"; a cross-origin hop cannot read it
  // back and the navigation becomes an honest goto rather than a guessed fold.
  // Whether the activated thing is the kind that NAVIGATES — a link, a submit control,
  // a form submission. A plain button is not, and that is the difference between a click
  // whose consequence is a page and a click the human happened to make before going
  // somewhere themselves.
  function isNavigational(element, kind) {
    if (kind === "submit") return true;
    if (!element || element.nodeType !== 1) return false;
    if (element.closest("a[href]")) return true;
    var control = element.closest("button, input");
    if (!control) return false;
    var type = (control.getAttribute("type") || "").toLowerCase();
    // A submit control submits a FORM. Outside one it navigates nothing, whatever its
    // type says — a bare <button> defaults to type=submit and is the commonest control
    // on a page that never navigates at all.
    if (!control.form) return false;
    if (control.tagName === "BUTTON") return type === "" || type === "submit";
    return type === "submit" || type === "image";
  }
  // FIRST WINS, until it is consumed or the human moves on. A second activation must not
  // overwrite an unconsumed one: when A navigates slowly and B happens in between, an
  // overwrite would hand A's page to B — the wrong step grows a document it never
  // reached. A's id stays, A's navigation claims it, and because A's step has closed by
  // then the navigation becomes an honest goto instead of B's consequence.
  function rememberActivation(actionId, navigational) {
    try {
      if (sessionStorage.getItem(ACTIVATION_KEY)) return;
      sessionStorage.setItem(
        ACTIVATION_KEY,
        JSON.stringify({
          actionId: actionId,
          frameId: frameId,
          navigational: Boolean(navigational)
        })
      );
    } catch (blocked) {
      // Storage denied (a sandboxed or partitioned document). Same-document causation
      // still works; a cross-document one degrades to a goto, which is the safe answer.
    }
  }
  function forgetActivation() {
    try {
      sessionStorage.removeItem(ACTIVATION_KEY);
    } catch (blocked) {
      // Nothing to forget.
    }
  }
  function takeStoredActivation() {
    try {
      var raw = sessionStorage.getItem(ACTIVATION_KEY);
      if (!raw) return null;
      sessionStorage.removeItem(ACTIVATION_KEY);
      return JSON.parse(raw);
    } catch (blocked) {
      return null;
    }
  }
  // A route change inside this document. It is the in-flight action's consequence when
  // one is in flight, and the human's when none is.
  //
  // One route change is not one event: Chromium fires hashchange AND popstate for a
  // hash assignment, and both again for a history entry the human went back to, so the
  // same move announced itself two and three times and the spec grew steps for
  // navigations that never happened. The URL is what changed, so the URL is what
  // decides whether anything did.
  var announcedUrl = null;
  function announceSameDocument() {
    if (location.href === announcedUrl) return;
    announcedUrl = location.href;
    // CONSUMED, not merely read: one activation owns at most one route change, so a
    // second delayed route cannot claim the same click a third time.
    var cause = pendingActivation;
    pendingActivation = null;
    emit("navigation", null, {
      sameDocument: true,
      causeActionId: cause === null ? undefined : cause
    });
  }
  // The value never leaves the page when the control holds a secret and the human did
  // not ask for it. Withholding here, rather than dropping it host-side, is the point:
  // a password that crossed the binding is already in another process's memory.
  function valuePayload(element, kind) {
    var found = classifyValueOf(element);
    // Withheld is the ABSENCE of a value beside a classification. One record, and the
    // shape of the payload says what was done with it — no second field to disagree.
    if (found && !window.__formicCaptureSecrets) {
      return { control: kind, classification: found };
    }
    if (found) return { control: kind, value: valueOf(element, kind), classification: found };
    return { control: kind, value: valueOf(element, kind) };
  }
  var ACTIONABLE =
    "button, a[href], input, select, textarea, label, [role], [data-testid]";
  function actionable(node) {
    if (!node || node.nodeType !== 1) return null;
    return node.closest(ACTIONABLE) || node;
  }
  // The control a label activates. The label element's own "control" property is the
  // browser's answer and covers both forms: a for= reference and a wrapped control.
  function labelControlOf(label) {
    if (label.tagName !== "LABEL") return null;
    if (label.control !== undefined) return label.control;
    var bound = label.getAttribute("for");
    return bound
      ? document.getElementById(bound)
      : label.querySelector("input, select, textarea");
  }
  var lastClickElement = null;
  var lastClickStamp = -1;
  function onClick(event) {
    var element = actionable(event.target);
    if (!element) return;
    // A select click is its popup OPENING, not a choice — only its change says which
    // option the human took, and emitting both made one choice two steps, the first of
    // which a replay cannot act on.
    if (element.tagName === "SELECT") return;
    // A label forwards its activation to the control it labels and Chromium dispatches
    // BOTH clicks, both trusted (measured), so the recording toggled a checkbox twice
    // and a replay left it exactly as it found it. One gesture, one step: the label's
    // click is the one the human made and the one a replay reproduces.
    // Same gesture is the browser's own statement, not a timer: a forwarded click
    // carries the IDENTICAL event.timeStamp as the label click that produced it
    // (measured — 111.30000019073486 on both).
    if (
      lastClickElement !== null &&
      lastClickStamp === event.timeStamp &&
      labelControlOf(lastClickElement) === element
    ) {
      return;
    }
    lastClickElement = element;
    lastClickStamp = event.timeStamp;
    emit("click", element);
  }
  // A checkbox or radio is TOGGLED by the click that is already recorded; its input and
  // change events describe the same one action and would replay as an unfillable fill.
  // A select is the mirror image: its click is the popup opening, and only its change
  // event says which option the human chose.
  function onInput(event) {
    var element = event.target;
    var kind = controlKind(element);
    if (kind === "" || kind === "checkable" || kind === "select") return;
    emit("input", element, valuePayload(element, kind));
  }
  function onChange(event) {
    var element = event.target;
    var kind = controlKind(element);
    if (kind === "" || kind === "checkable") return;
    emit("change", element, valuePayload(element, kind));
  }
  function onSubmit(event) {
    var form = event.target;
    var submitter = event.submitter || null;
    // A submitted form and the control that submitted it are ONE activation. Minting a
    // second id here meant the host discarded the submit (its click is already a step)
    // and the navigation it caused pointed at an id no step carried, so a form's own
    // navigation could never fold into the click that sent it.
    // No submitter means no control was activated: the human pressed Enter in a field,
    // which is an action of its own and has no click to be folded into.
    var subject = submitter || document.activeElement || form;
    emit("submit", subject, {
      submitter: Boolean(submitter),
      reuseActionId: submitter ? pendingActivation : undefined
    });
  }
  var historyPatched = false;
  function patchHistory() {
    if (historyPatched) return;
    historyPatched = true;
    // Only a document load used to announce a navigation, so a single-page application
    // moved between screens and the recording said nothing happened at all.
    var pushState = history.pushState;
    var replaceState = history.replaceState;
    history.pushState = function () {
      var result = pushState.apply(history, arguments);
      announceSameDocument();
      return result;
    };
    history.replaceState = function () {
      var result = replaceState.apply(history, arguments);
      announceSameDocument();
      return result;
    };
  }
  function start() {
    window.__STATE_HOOK__ = visibleState;
    patchHistory();
    window.removeEventListener("hashchange", announceSameDocument);
    window.removeEventListener("popstate", announceSameDocument);
    window.addEventListener("hashchange", announceSameDocument);
    window.addEventListener("popstate", announceSameDocument);
    document.removeEventListener("click", onClick, true);
    document.removeEventListener("input", onInput, true);
    document.removeEventListener("change", onChange, true);
    document.removeEventListener("submit", onSubmit, true);
    document.addEventListener("click", onClick, true);
    document.addEventListener("input", onInput, true);
    document.addEventListener("change", onChange, true);
    document.addEventListener("submit", onSubmit, true);
  }
  window.__INSTALL_HOOK__ = start;
  window.__TEARDOWN_HOOK__ = function () {
    window.removeEventListener("hashchange", announceSameDocument);
    window.removeEventListener("popstate", announceSameDocument);
    document.removeEventListener("click", onClick, true);
    document.removeEventListener("input", onInput, true);
    document.removeEventListener("change", onChange, true);
    document.removeEventListener("submit", onSubmit, true);
  };
  // Arm only while a recording is actually running. The host owns that answer.
  function armIfRecording() {
    var ask = window.__ACTIVE_BINDING__;
    if (typeof ask !== "function") return;
    Promise.resolve(ask()).then(function (active) {
      if (active) announce();
    }, function () {});
  }
  function announce() {
    start();
    // document.referrer is the browser saying who initiated this navigation: the page
    // before it (a link, a submission, a scripted assignment — however long it took to
    // arrive), or nobody, which means the human typed an address or the host drove it.
    announcedUrl = location.href;
    // The id the document before this one left behind IS the answer. The referrer only
    // corroborates it — a no-referrer policy says nothing about who clicked, and a
    // referrer says nothing about WHICH action started the load.
    var held = takeStoredActivation();
    var referred = document.referrer !== "";
    // The id is the decider; the referrer only CORROBORATES it. An activation on a link
    // or a submit control navigates by its own nature and needs no corroboration — which
    // is what keeps a Referrer-Policy of no-referrer from splitting a real click's
    // navigation into a redundant goto. An ordinary button's activation needs the
    // browser to agree the page started this load, or it is the human's own move.
    var owned = held !== null && (held.navigational === true || referred);
    emit("navigation", null, {
      causeActionId: owned ? held.actionId : undefined,
      referred: referred
    });
  }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", armIfRecording, { once: true });
  } else {
    armIfRecording();
  }
})();
`;

/**
 * The attributes an `<iframe>` can be addressed by, in the order a reader would choose:
 * the id the app gave it, the id it gave for testing, then the name — every one of them
 * a stable, authored handle rather than a position in the markup.
 */
const FRAME_SELECTOR_ATTRIBUTES: readonly string[] = [
  "id",
  "data-testid",
  "name",
];

/** Selector candidates for an `<iframe>`, from its own attributes. */
function frameSelectorCandidates(
  attributes: Record<string, string | null>,
): string[] {
  const candidates: string[] = [];
  const id = attributes.id;
  if (
    id !== null &&
    id !== undefined &&
    id !== "" &&
    /^[A-Za-z][\w-]*$/.test(id)
  )
    candidates.push(`#${id}`);
  for (const attribute of ["data-testid", "name"]) {
    const value = attributes[attribute];
    if (value === null || value === undefined || value === "") continue;
    if (controlCharacterIn(value) !== null) continue;
    candidates.push(`iframe[${attribute}="${escapeCssAttributeValue(value)}"]`);
  }
  return candidates;
}

/**
 * ONE link of the chain: how to name `child` from inside `parent`, PROVEN.
 *
 * The owning `<iframe>` element is the strongest answer, so it is tried first — and it
 * is proven the way every other emitted locator is: the candidate must resolve to
 * exactly one element in the parent's document, and that element must be the one that
 * owns this very frame. A candidate that merely looks unique is not a locator.
 *
 * `frameElement()` is what makes that possible, and it is also what a CROSS-ORIGIN child
 * takes away: the element lives in the parent's document, but Playwright cannot hand it
 * back across the process boundary in every browser and version, and nothing here is
 * willing to emit an unproven selector. So the fallbacks are the frame's own facts —
 * its `name`, then its document URL as a prefix — neither of which needs the parent's
 * DOM at all. A frame with none of the three is refused BY NAME, which is what the whole
 * path used to do for every frame.
 */
/**
 * ONE link's FALLBACK, decided synchronously against the frame tree as it is right now.
 *
 * Synchronously is the whole point. `name()` and `url()` are read off a LIVE frame, and
 * the walk that used to read them ran after an await — so a click queued while the frame
 * was on /before was named with /after's URL, silently, and the step then addressed a
 * document the human never touched. The facts are captured when the document is first
 * seen and the walk consults the capture, so a queued event is named by the document it
 * actually came from.
 *
 * The selector form needs no snapshot: it is proven against the PARENT's DOM, and the
 * owning `<iframe>` element survives its own document's navigation.
 */
function frameLinkFallback(
  parent: Frame,
  child: Frame,
): FrameRef | { refuse: string } {
  const siblings = parent.childFrames();
  const name = child.name();
  if (name !== "") {
    const sharing = siblings.filter((frame) => frame.name() === name).length;
    if (sharing === 1) return { name };
    return {
      refuse: `frame not uniquely addressable: ${sharing} siblings share name ${JSON.stringify(name)}`,
    };
  }
  const url = child.url();
  if (/^https?:\/\//i.test(url)) {
    const sharing = siblings.filter((frame) =>
      frame.url().startsWith(url),
    ).length;
    if (sharing === 1) return { urlPrefix: url };
    return {
      refuse: `frame not uniquely addressable: ${sharing} siblings share url prefix ${JSON.stringify(url)}`,
    };
  }
  return {
    refuse:
      "the frame has no name, no reachable owning element and no http(s) document URL, so nothing in the spec grammar can address it",
  };
}

/** One link of the path from the page down to a document, as it stood when that document
 *  was first seen. */
interface FrameLinkFacts {
  parent: Frame;
  child: Frame;
  fallback: FrameRef | { refuse: string };
}

/**
 * The whole path to `frame`, outermost first, snapshotted NOW.
 *
 * Taken in the binding handler, before the event joins the delivery queue — the earliest
 * moment the host can see, and the only one still describing the document the event came
 * from.
 */
export function framePathFacts(page: Page, frame: Frame): FrameLinkFacts[] {
  const path: FrameLinkFacts[] = [];
  let current: Frame | null = frame;
  while (current !== null && current !== page.mainFrame()) {
    const parent: Frame | null = current.parentFrame();
    if (parent === null) break;
    path.unshift({
      parent,
      child: current,
      fallback: frameLinkFallback(parent, current),
    });
    current = parent;
  }
  return path;
}

/** A selector for the owning `<iframe>`, proven against the element itself, or null. */
async function provenFrameSelector(
  parent: Frame,
  child: Frame,
): Promise<string | null> {
  let element: ElementHandle<Node> | null = null;
  try {
    element = await child.frameElement();
    const owner = element;
    const attributes = await owner.evaluate(
      (node: Element, names: readonly string[]) => {
        const read: Record<string, string | null> = {};
        for (const name of names) read[name] = node.getAttribute(name);
        return read;
      },
      FRAME_SELECTOR_ATTRIBUTES,
    );
    for (const candidate of frameSelectorCandidates(attributes)) {
      const matches = parent.locator(candidate);
      if ((await matches.count()) !== 1) continue;
      const resolved = await matches.elementHandle();
      if (resolved === null) continue;
      const same = await parent.evaluate(([one, other]) => one === other, [
        element,
        resolved,
      ] as const);
      if (same) return candidate;
    }
    return null;
  } catch {
    // A cross-origin child, or one that detached mid-walk. Nothing is guessed in its
    // place: the caller falls back to a fact the frame answers for itself.
    return null;
  } finally {
    await element?.dispose().catch(() => {});
  }
}

/**
 * The whole chain from the page down to `frame`, outermost first — or null when some
 * link cannot be named at all.
 *
 * Cached PER DOCUMENT, never per `Frame`: a human clicking five times inside one iframe
 * would otherwise walk and prove the same chain five times, on the event path, while the
 * page is live — but a `Frame` object OUTLIVES the document in it, so the key is the
 * document's own id, the one the bundle mints per document and stamps on every event.
 *
 * The name/url half of every link comes from `path`, which was snapshotted when this
 * document was first seen; only the selector proof consults the live DOM, and it may,
 * because the owning `<iframe>` element survives its own document's navigation.
 */
async function frameChainFor(
  documentId: string,
  path: FrameLinkFacts[],
  cache: Map<string, FrameChain | { refuse: string }>,
): Promise<FrameChain | { refuse: string }> {
  const known = cache.get(documentId);
  if (known !== undefined) return known;
  const chain: FrameChain = [];
  for (const link of path) {
    const proven = await provenFrameSelector(link.parent, link.child);
    if (proven !== null) {
      chain.push({ selector: proven });
      continue;
    }
    if ("refuse" in link.fallback) {
      cache.set(documentId, link.fallback);
      return link.fallback;
    }
    chain.push(link.fallback);
  }
  cache.set(documentId, chain);
  return chain;
}

/**
 * Turn the page's report into facts a spec may carry — or refuse it.
 *
 * The page proves every CSS candidate it emits, but it cannot answer for the ROLE and
 * the NAME: a second HTML-to-ARIA implementation lived here once, calling every input a
 * textbox and every select a combobox, so what it reported did not resolve in the engine
 * a replay uses. Playwright owns that model, so the host asks it — through the temporary
 * probe attribute the page stamped — and then PROVES the answer: the role locator must
 * resolve exactly one element, and it must be the same element the event was about.
 *
 * Anything unproven is dropped rather than emitted. A target left with no CSS candidate
 * at all is refused by its caller, with a warning naming it: an unreplayable locator in
 * a committed spec is worse than a recording the human is told is incomplete.
 */
export async function proveTargetFacts(
  page: Page | Frame,
  facts: TargetFacts,
): Promise<TargetFacts> {
  const probe = facts.probe;
  const { probe: _dropped, ...proven } = facts;
  if (probe === undefined) return proven;
  const probeSelector = `[${PROBE_ATTRIBUTE}="${escapeCssAttributeValue(probe)}"]`;
  try {
    const target = await page.$(probeSelector);
    if (target === null) return proven;
    const named = parseAriaLine(
      await page.locator(probeSelector).ariaSnapshot(),
    );
    if (named === null) return proven;
    const byRole = page.getByRole(
      named.role as Parameters<Page["getByRole"]>[0],
      { name: named.name, exact: true },
    );
    if ((await byRole.count()) !== 1) return proven;
    const resolved = await byRole.elementHandle();
    if (resolved === null) return proven;
    const same = await page.evaluate(([one, other]) => one === other, [
      target,
      resolved,
    ] as const);
    if (!same) return proven;
    return named.name === ""
      ? { ...proven, role: named.role }
      : { ...proven, role: named.role, name: named.name };
  } catch {
    // A page that navigated out from under the probe cannot answer for it. Nothing is
    // guessed in its place — the facts stand as the page proved them, and a target left
    // with none refuses its step.
    return proven;
  } finally {
    await page
      .evaluate(
        (selector: string) =>
          document
            .querySelectorAll(selector)
            .forEach((node) => node.removeAttribute("data-formic-probe")),
        probeSelector,
      )
      .catch(() => {});
  }
}

/** The temporary attribute the page stamps on a target so the host can address it. */
const PROBE_ATTRIBUTE = "data-formic-probe";

/**
 * The role and accessible name out of the FIRST line of an aria snapshot.
 *
 * Playwright writes one node per line as `- role "name"`, so the element the snapshot was
 * taken on is its first line and everything after it is that element's subtree.
 */
function parseAriaLine(
  snapshot: string,
): { role: string; name: string } | null {
  const first = snapshot.split("\n").find((line) => line.trim() !== "");
  if (first === undefined) return null;
  const matched = /^\s*-\s+([a-zA-Z]+)(?:\s+"((?:[^"\\]|\\.)*)")?/.exec(first);
  if (matched === null) return null;
  return {
    role: matched[1],
    name: (matched[2] ?? "").replace(/\\(.)/g, "$1"),
  };
}

/**
 * WHICH document, and how much it has sent — in ONE round trip.
 *
 * These were two separate reads, and a navigation landing between them paired the OLD
 * document's id with the NEW document's low sequence: a mark no document ever had. The
 * barrier then compared that count against the old document's delivery, which had passed
 * it long ago, and ended the drain while the new document's events were still in flight.
 * A single evaluate cannot tear — whichever document answers, both halves are its own.
 */
export async function readDocumentMark(
  page: Page | Frame,
): Promise<{ frameId: string; seq: number }> {
  try {
    const read = (
      names: [string, string],
    ): { frameId: string; seq: number } => {
      const hooks = globalThis as unknown as Record<string, () => unknown>;
      const frameId = hooks[names[0]]?.() ?? "";
      const seq = hooks[names[1]]?.() ?? 0;
      return {
        frameId: typeof frameId === "string" ? frameId : "",
        seq: typeof seq === "number" ? seq : 0,
      };
    };
    return await page.evaluate(read, [FRAME_HOOK, SEQ_HOOK] as [
      string,
      string,
    ]);
  } catch {
    return { frameId: "", seq: 0 };
  }
}

/**
 * The mark of EVERY document the page currently holds, child frames included.
 *
 * The barrier used to read the main document alone, which was exactly right while a child
 * frame's events were refused: there were none to wait for. Now that a frame's clicks
 * become steps, a mark that ignores child documents ends the drain while the human's last
 * action — the one the barrier exists to save — is still crossing from an iframe.
 *
 * A frame with no bundle on it answers `{ frameId: "", seq: 0 }`, which is satisfied by
 * construction and costs the drain nothing.
 */
export async function readDocumentMarks(
  page: Page,
): Promise<{ frameId: string; seq: number }[]> {
  const marks = await Promise.all(
    page.frames().map((frame) => readDocumentMark(frame)),
  );
  return marks.filter((mark) => mark.frameId !== "");
}

/** Which document the bundle is running in, or "" when no bundle answers. */
export async function readFrameId(page: Page): Promise<string> {
  try {
    const read = (name: string): unknown =>
      (globalThis as unknown as Record<string, () => string>)[name]?.() ?? "";
    const found = await page.evaluate(read, FRAME_HOOK);
    return typeof found === "string" ? found : "";
  } catch {
    return "";
  }
}

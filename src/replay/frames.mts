/**
 * A frame chain → the live browsing context it names.
 *
 * The chain is resolved to a Playwright `Frame`, not to a `FrameLocator`, and that is
 * the load-bearing choice here. A `Frame` carries the same action and locator surface a
 * `Page` does — `click`, `fill`, `press`, `selectOption`, `locator`, `getByRole` — so
 * everything downstream (the action table in runner.mts, the locator table in
 * assertions.mts, the target proof in capture/events.mts) runs UNCHANGED against a
 * frame. A `FrameLocator` would have forced a second copy of each, and two copies of a
 * mapping is how replay and export come to disagree about what a spec means.
 *
 * It is also the only resolution that can express all three `FrameRef` forms:
 * `frameLocator` takes a selector and nothing else, while a frame named by `name` or by
 * its document URL is found by asking the PARENT for its children.
 *
 * Every refusal names the LINK that failed, by position and by what it asked for. A
 * chain is read outermost-first, so "the second link" is the only way to tell a reader
 * which `<iframe>` to go and look at — and a step that fails because its frame is gone
 * must never read like a step whose locator is stale.
 */
import type { Frame, Page } from "playwright-core";
import type { FrameChain, FrameRef } from "../spec/types.mts";

/** Anything a locator can be resolved against: the page's main frame, or a nested one. */
export type LocatorScope = Page | Frame;

/** Thrown when a chain does not name a frame that is open right now. */
export class FrameChainError extends Error {
  constructor(
    readonly position: number,
    readonly link: FrameRef,
    reason: string,
  ) {
    super(
      `frame chain link ${position + 1} (${JSON.stringify(link)}) did not resolve — ${reason}`,
    );
    this.name = "FrameChainError";
  }
}

/**
 * The frame owned by the `<iframe>` this link's selector addresses IN THE PARENT's
 * document — the strongest form, because the element is proven where it lives.
 *
 * COUNTED, not `.first()`. A selector is no more allowed to stand for two frames than a
 * name is: `iframe[data-widget]` on a page carrying two of them silently ran the step in
 * whichever the engine met first, with every locator inside it resolving, which is the
 * exact misattribution the name and url forms are already refused for. Returning the
 * count lets the caller refuse it by name, and lets a not-yet-attached frame (count 0)
 * keep polling.
 */
async function framesBySelector(
  parent: LocatorScope,
  selector: string,
): Promise<{ matches: number; frame: Frame | null }> {
  const locator = parent.locator(selector);
  const matches = await locator.count();
  if (matches !== 1) return { matches, frame: null };
  const element = await locator.elementHandle();
  if (element === null) return { matches, frame: null };
  return { matches, frame: await element.contentFrame() };
}

/**
 * The child frame this link names, among the parent's OWN children.
 *
 * Scoped to the parent deliberately: `page.frame({ name })` searches the whole page, so
 * a chain that says "the frame called `payment` inside the frame called `checkout`"
 * would have matched a `payment` frame anywhere at all — a chain whose links are not
 * actually nested is not the chain the spec wrote down.
 */
function childFramesOf(parent: LocatorScope, link: FrameRef): Frame[] {
  const children =
    "childFrames" in parent
      ? parent.childFrames()
      : parent.mainFrame().childFrames();
  return children.filter((child) => {
    if (link.name !== undefined) return child.name() === link.name;
    if (link.url !== undefined) return child.url() === link.url;
    return child.url().startsWith(link.urlPrefix as string);
  });
}

/** How this link asked for its frame, for a refusal a reader can act on. */
function describe(link: FrameRef): string {
  if (link.selector !== undefined)
    return `no <iframe> matching ${JSON.stringify(link.selector)} is open in the frame that should contain it`;
  if (link.name !== undefined)
    return `no child frame is named ${JSON.stringify(link.name)}`;
  if (link.url !== undefined)
    return `no child frame's document URL is ${JSON.stringify(link.url)}`;
  return `no child frame's document URL starts with ${JSON.stringify(link.urlPrefix)}`;
}

/** How often a chain re-asks for a frame that is not open yet. */
const POLL_MS = 50;

/**
 * ONE link, resolved with the step's own patience.
 *
 * A frame attaches when the page decides to, and a chain asked once got a frame that was
 * a few milliseconds away from existing — the step failed with a message about a frame
 * the reader could see in the browser. A selector link inherits Playwright's own waiting;
 * a name/url link has none of its own, so it polls to the same deadline. AMBIGUITY does
 * not poll: more frames can only arrive, so waiting cannot resolve it, and the answer is
 * already known.
 */
async function resolveLink(
  scope: LocatorScope,
  link: FrameRef,
  position: number,
  deadline: number,
): Promise<Frame | null> {
  let lastFailure = "";
  for (;;) {
    let matches = 0;
    if (link.selector !== undefined) {
      try {
        const found = await framesBySelector(scope, link.selector);
        matches = found.matches;
        if (found.frame !== null) return found.frame;
      } catch (caught) {
        lastFailure = ` (${(caught as Error).message.split("\n")[0]})`;
      }
    } else {
      const found = childFramesOf(scope, link);
      matches = found.length;
      if (found.length === 1) return found[0];
    }
    // AMBIGUITY does not poll, whichever form the link took: more frames can only
    // arrive, so waiting cannot resolve it, and the answer is already known.
    if (matches > 1) {
      throw new FrameChainError(
        position,
        link,
        `it is ambiguous — ${matches} frames match`,
      );
    }
    if (Date.now() >= deadline) {
      if (lastFailure !== "")
        throw new FrameChainError(
          position,
          link,
          `${describe(link)}${lastFailure}`,
        );
      return null;
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_MS));
  }
}

/**
 * Walks the chain outermost-first and returns the frame it names.
 *
 * The page itself is the scope for an ABSENT chain; callers pass `undefined` for a step
 * that addresses the top-level page, which is every step written before this field
 * existed.
 */
export async function resolveFrameChain(
  page: Page,
  chain: FrameChain | undefined,
  timeoutMs: number,
): Promise<LocatorScope> {
  if (chain === undefined || chain.length === 0) return page;
  let scope: LocatorScope = page;
  // ONE deadline for the whole chain, not one per link: the step's timeout is how long
  // the step may take, and a five-link chain must not be allowed to take five times it.
  const deadline = Date.now() + timeoutMs;
  for (const [position, link] of chain.entries()) {
    const next = await resolveLink(scope, link, position, deadline);
    if (next === null)
      throw new FrameChainError(position, link, describe(link));
    scope = next;
  }
  return scope;
}

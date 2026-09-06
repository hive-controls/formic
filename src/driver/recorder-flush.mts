/**
 * The recorder flush every rrweb-backed session owes its caller before handing over
 * the event stream.
 *
 * rrweb emits through a MutationObserver and the binding hop is asynchronous, so the
 * last action's events can still be in flight when the harness asks for the stream.
 * One animation frame lets the observer fire; the grace covers the binding round trip.
 * ⚠️ ASSUMED sufficient — bounded by measurement if a segment ever comes back short.
 *
 * One implementation, two callers: the local Playwright backend (local-playwright.mts)
 * and the Playwright Test fixture's session (playwright/fixture.mts), which record
 * through the same injected recorder and therefore owe the same flush.
 */
import type { Page } from "playwright-core";

export const FLUSH_GRACE_MS = 50;

/** Waits for the page's in-flight rrweb events to land. A closed page has nothing
 *  left to flush, and an evaluate against a torn-down context must never surface as
 *  a replay failure — both are silent no-ops. */
export async function flushRecorder(page: Page): Promise<void> {
  if (page.isClosed()) return;
  await page
    .evaluate(() => new Promise((resolve) => requestAnimationFrame(resolve)))
    .catch(() => {});
  await new Promise((resolve) => setTimeout(resolve, FLUSH_GRACE_MS));
}

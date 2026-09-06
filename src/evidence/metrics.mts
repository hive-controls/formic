/**
 * Per-step performance metrics — track and trend, never assert.
 *
 * Navigation timing, paint timing and LCP are only meaningful for the `goto` step that
 * caused them, so they are `null` on every other step. CLS and long tasks accumulate
 * for EVERY step, scoped to that step's own window.
 *
 * "Window" here is the same half-open boundary segment.mts's INVARIANT 1 uses for replay
 * segments: a step's window closes when the NEXT step's action is about to run (or when
 * the replay ends, for the last step) — not at the step's own `endedAt`. Playwright
 * actions return before their consequences (a delayed layout shift, a navigation's paint)
 * settle, so closing early would drop them into the wrong step or lose them outright.
 *
 * The collector is a page-side buffer that DRAINS on close, which sidesteps timestamp
 * math entirely: whatever accumulated since the last drain belongs to the step whose
 * window is closing now. A `goto` step's own navigation/paint/LCP entries are read at
 * that same close — by then the navigation has fully settled (the runner awaits two
 * animation frames after `page.goto`), and the NEXT step's action has not run yet, so
 * the document is still the one this step produced.
 *
 * Best-effort throughout: a gate that cannot paint, a page that throws on evaluate, or
 * an unsupported PerformanceObserver entry type all report `null` fields, never 0 and
 * never a thrown error into the runner.
 */
import type { Page } from "playwright-core";
import type { StepMetrics } from "./types.mts";

export type { StepMetrics } from "./types.mts";

export const EMPTY_STEP_METRICS: Readonly<StepMetrics> = Object.freeze({
  ttfb: null,
  domContentLoaded: null,
  domComplete: null,
  firstPaint: null,
  firstContentfulPaint: null,
  lcp: null,
  cls: null,
  longTasksCount: null,
  longTasksMs: null,
});

/** One layout-shift entry as drained from the page: value plus its own start time, so
 *  session windows can be grouped correctly even though drains are irregular. */
interface LayoutShiftSample {
  value: number;
  time: number;
}

interface RawDrain {
  support: { cls: boolean; longtask: boolean; lcp: boolean };
  layoutShifts: LayoutShiftSample[];
  longTasks: number[];
  lcp: number | null;
  nav: {
    responseStart: number;
    domContentLoadedEventEnd: number;
    domComplete: number;
  } | null;
  paint: Record<string, number> | null;
}

/** Installed as a page-scoped init script BEFORE the first navigation, so it re-runs on
 *  every document the page loads (each `goto`) and observes from document start. */
const COLLECTOR_SCRIPT = `
(() => {
  const state = {
    support: { cls: false, longtask: false, lcp: false },
    layoutShifts: [],
    longTasks: [],
    lcp: null,
  };
  window.__e2edocMetrics = state;
  try {
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        if (!entry.hadRecentInput) {
          state.layoutShifts.push({ value: entry.value, time: entry.startTime });
        }
      }
    }).observe({ type: "layout-shift", buffered: true });
    state.support.cls = true;
  } catch (e) { /* unsupported entry type */ }
  try {
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) state.longTasks.push(entry.duration);
    }).observe({ type: "longtask", buffered: true });
    state.support.longtask = true;
  } catch (e) { /* unsupported entry type */ }
  try {
    new PerformanceObserver((list) => {
      const entries = list.getEntries();
      const last = entries[entries.length - 1];
      if (last) state.lcp = last.startTime;
    }).observe({ type: "largest-contentful-paint", buffered: true });
    state.support.lcp = true;
  } catch (e) { /* unsupported entry type */ }
})();`;

/** Adds the collector for every future navigation. Must be called before the first
 *  step runs — the runner's page starts blank, so this always precedes it. Best-effort
 *  like every other function here: a page double with no `addInitScript` (a hand-built
 *  test session) must not stop a replay, it just collects no metrics. */
export async function installMetricsCollector(page: Page): Promise<void> {
  try {
    await page.addInitScript({ content: COLLECTOR_SCRIPT });
  } catch {
    /* no metrics collection this run */
  }
}

/** Drains the page-side buffers and reads the current document's navigation/paint
 *  entries. `null` on any evaluate failure — a closed page, a gate with no JS engine
 *  hook, or a collector that never installed (an older init-script race). */
async function drainPageMetrics(page: Page): Promise<RawDrain | null> {
  try {
    return await page.evaluate<RawDrain>(() => {
      const state = (
        window as unknown as {
          __e2edocMetrics?: RawDrain & { support: RawDrain["support"] };
        }
      ).__e2edocMetrics;
      if (!state) {
        return {
          support: { cls: false, longtask: false, lcp: false },
          layoutShifts: [],
          longTasks: [],
          lcp: null,
          nav: null,
          paint: null,
        };
      }
      const layoutShifts = state.layoutShifts.splice(0);
      const longTasks = state.longTasks.splice(0);
      const navEntry = performance.getEntriesByType("navigation")[0] as
        PerformanceNavigationTiming | undefined;
      const paintEntries = performance.getEntriesByType("paint");
      return {
        support: { ...state.support },
        layoutShifts,
        longTasks,
        lcp: state.lcp,
        nav: navEntry
          ? {
              responseStart: navEntry.responseStart,
              domContentLoadedEventEnd: navEntry.domContentLoadedEventEnd,
              domComplete: navEntry.domComplete,
            }
          : null,
        paint: paintEntries.length
          ? Object.fromEntries(
              paintEntries.map((entry) => [entry.name, entry.startTime]),
            )
          : null,
      };
    });
  } catch {
    return null;
  }
}

/**
 * Cumulative Layout Shift, official session-window algorithm: consecutive shifts are
 * grouped while the gap since the previous entry is under 1s and the window since the
 * first entry is under 5s; CLS is the largest such session's summed value. Scoped here
 * to one step's window rather than the whole page life.
 */
export function sumSessionWindowedCls(entries: LayoutShiftSample[]): number {
  let maxSessionValue = 0;
  let sessionValue = 0;
  let sessionFirstTime = -Infinity;
  let sessionLastTime = -Infinity;
  for (const entry of entries) {
    if (
      entry.time - sessionLastTime > 1000 ||
      entry.time - sessionFirstTime > 5000
    ) {
      sessionValue = 0;
      sessionFirstTime = entry.time;
    }
    sessionLastTime = entry.time;
    sessionValue += entry.value;
    maxSessionValue = Math.max(maxSessionValue, sessionValue);
  }
  return maxSessionValue;
}

/** Closes the window for a step whose action was `action` — drains the collector and
 *  shapes a StepMetrics. Never throws; an evaluate failure yields all-null metrics. */
export async function closeStepMetrics(
  page: Page,
  action: string,
): Promise<StepMetrics> {
  const raw = await drainPageMetrics(page);
  if (!raw) return { ...EMPTY_STEP_METRICS };
  const isGoto = action === "goto";
  return {
    ttfb: isGoto ? (raw.nav?.responseStart ?? null) : null,
    domContentLoaded: isGoto
      ? (raw.nav?.domContentLoadedEventEnd ?? null)
      : null,
    domComplete: isGoto ? (raw.nav?.domComplete ?? null) : null,
    firstPaint: isGoto ? (raw.paint?.["first-paint"] ?? null) : null,
    firstContentfulPaint: isGoto
      ? (raw.paint?.["first-contentful-paint"] ?? null)
      : null,
    lcp: isGoto && raw.support.lcp ? raw.lcp : null,
    cls: raw.support.cls ? sumSessionWindowedCls(raw.layoutShifts) : null,
    longTasksCount: raw.support.longtask ? raw.longTasks.length : null,
    longTasksMs: raw.support.longtask
      ? raw.longTasks.reduce((sum, duration) => sum + duration, 0)
      : null,
  };
}

/**
 * Unit coverage for the metrics module's own logic — the CLS session-window algorithm
 * and how a drained page-side reading is shaped into a StepMetrics — with a fake `Page`
 * standing in for the real browser evaluate boundary. The real browser path (a genuine
 * PerformanceObserver, a real goto) is exercised in replay/runner.test.mts against a
 * real Chromium and the sample app.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import type { Page } from "playwright-core";
import {
  closeStepMetrics,
  installMetricsCollector,
  sumSessionWindowedCls,
} from "./metrics.mts";

function fakePage(evaluateResult: unknown): Page {
  return {
    evaluate: async () => evaluateResult,
  } as unknown as Page;
}

test("sumSessionWindowedCls groups shifts within a 1s gap into one session", () => {
  const cls = sumSessionWindowedCls([
    { value: 0.05, time: 0 },
    { value: 0.03, time: 400 },
    { value: 0.02, time: 900 },
  ]);
  assert.equal(cls, 0.1);
});

test("sumSessionWindowedCls starts a new session after a gap over 1s", () => {
  const cls = sumSessionWindowedCls([
    { value: 0.2, time: 0 },
    // gap of 1200ms from the previous entry — new session, and it is smaller, so the
    // FIRST session's 0.2 is still the max, not a summed 0.25.
    { value: 0.05, time: 1200 },
  ]);
  assert.equal(cls, 0.2);
});

test("sumSessionWindowedCls starts a new session once a session exceeds 5s even with no gap", () => {
  const cls = sumSessionWindowedCls([
    { value: 0.1, time: 0 },
    { value: 0.1, time: 900 },
    // still under the 1s gap rule, but 5200ms since the session's first entry — a new
    // session starts here even though back-to-back entries never paused for 1s.
    { value: 0.4, time: 5200 },
  ]);
  assert.equal(cls, 0.4);
});

test("sumSessionWindowedCls returns 0 (a real measurement, not absence) for no entries", () => {
  assert.equal(sumSessionWindowedCls([]), 0);
});

test("closeStepMetrics: a goto step surfaces nav/paint/LCP; CLS and long tasks track on every step", async () => {
  const page = fakePage({
    support: { cls: true, longtask: true, lcp: true },
    layoutShifts: [{ value: 0.01, time: 0 }],
    longTasks: [60, 40],
    lcp: 812,
    nav: {
      responseStart: 12,
      domContentLoadedEventEnd: 88,
      domComplete: 140,
    },
    paint: { "first-paint": 30, "first-contentful-paint": 34 },
  });
  const metrics = await closeStepMetrics(page, "goto");
  assert.deepEqual(metrics, {
    ttfb: 12,
    domContentLoaded: 88,
    domComplete: 140,
    firstPaint: 30,
    firstContentfulPaint: 34,
    lcp: 812,
    cls: 0.01,
    longTasksCount: 2,
    longTasksMs: 100,
  });
});

test("closeStepMetrics: a non-goto step never carries navigation/paint/LCP, even when the drain has them", async () => {
  // Same raw drain as the goto case above — proves the suppression is keyed on the
  // step's OWN action, not on what happens to be in the document at close time.
  const page = fakePage({
    support: { cls: true, longtask: true, lcp: true },
    layoutShifts: [],
    longTasks: [],
    lcp: 812,
    nav: { responseStart: 12, domContentLoadedEventEnd: 88, domComplete: 140 },
    paint: { "first-paint": 30, "first-contentful-paint": 34 },
  });
  const metrics = await closeStepMetrics(page, "click");
  assert.equal(metrics.ttfb, null);
  assert.equal(metrics.domContentLoaded, null);
  assert.equal(metrics.domComplete, null);
  assert.equal(metrics.firstPaint, null);
  assert.equal(metrics.firstContentfulPaint, null);
  assert.equal(metrics.lcp, null);
  // CLS/long tasks are still real zero measurements, not absence.
  assert.equal(metrics.cls, 0);
  assert.equal(metrics.longTasksCount, 0);
  assert.equal(metrics.longTasksMs, 0);
});

test("closeStepMetrics: an unsupported entry type reports null, never 0", async () => {
  const page = fakePage({
    support: { cls: false, longtask: false, lcp: false },
    layoutShifts: [],
    longTasks: [],
    lcp: null,
    nav: { responseStart: 12, domContentLoadedEventEnd: 88, domComplete: 140 },
    paint: { "first-paint": 30, "first-contentful-paint": 34 },
  });
  const metrics = await closeStepMetrics(page, "goto");
  assert.equal(metrics.cls, null);
  assert.equal(metrics.longTasksCount, null);
  assert.equal(metrics.longTasksMs, null);
  assert.equal(metrics.lcp, null, "lcp unsupported even on a goto step");
});

test("closeStepMetrics never throws — a page that cannot evaluate yields all-null metrics", async () => {
  const brokenPage = {
    evaluate: async () => {
      throw new Error("page closed");
    },
  } as unknown as Page;
  const metrics = await closeStepMetrics(brokenPage, "goto");
  for (const value of Object.values(metrics)) assert.equal(value, null);
});

test("installMetricsCollector never throws when the page has no addInitScript", async () => {
  const bareFakePage = {} as unknown as Page;
  await assert.doesNotReject(installMetricsCollector(bareFakePage));
});

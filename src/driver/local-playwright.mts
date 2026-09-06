/**
 * Local Playwright backend — the seam's proof that Solari is replaceable.
 *
 * This backend exists so the abstraction is exercised, not aspirational: the capture,
 * replay, AND evidence paths run with no Solari key and no network. Evidence comes
 * from rrweb injected into the page (driver/rrweb-recorder.mts), emitting the same
 * event shape Solari's server-side recorder does.
 */
import { chromium, type Browser } from "playwright-core";
import type { Driver, DriverSession } from "./types.mts";
import { attachRrwebRecorder } from "./rrweb-recorder.mts";
import { flushRecorder } from "./recorder-flush.mts";

export interface LocalPlaywrightDriverOptions {
  headless?: boolean;
}

export class LocalPlaywrightDriver implements Driver {
  readonly name = "local-playwright";
  readonly canRecord = true;

  constructor(private readonly options: LocalPlaywrightDriverOptions = {}) {}

  async open(): Promise<DriverSession> {
    const browser: Browser = await chromium.launch({
      headless: this.options.headless ?? true,
    });
    const context = await browser.newContext();
    // Before the first page exists, so the init script covers every document.
    const recorder = await attachRrwebRecorder(context);
    const page = await context.newPage();

    return {
      sessionId: `local-${Date.now()}`,
      page,
      async fetchReplay() {
        await flushRecorder(page);
        return recorder.events();
      },
      async close() {
        await browser.close();
      },
    };
  }
}

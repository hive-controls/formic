/**
 * Frames: PNGs of each player on an evidence page, rendered by a real browser.
 *
 * GitHub renders images in a PR body but not an HTML page, so the repair PR shows the
 * BEFORE and AFTER frames inline and links to the page for the full replay. Rendering
 * with a real browser is also the strongest renderability proof there is: a segment
 * that cannot render produces no frame, and the PR generator refuses to proceed.
 */
import { mkdirSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";
import { chromium } from "playwright-core";

export interface RenderedFrame {
  name: string;
  file: string;
}

export async function renderFrames(
  pageFile: string,
  outDir: string,
  options: { timeoutMs?: number } = {},
): Promise<RenderedFrame[]> {
  mkdirSync(outDir, { recursive: true });
  const browser = await chromium.launch({ headless: true });
  try {
    const page = await browser.newPage({
      viewport: { width: 1400, height: 1000 },
    });
    // Hermetic: a replayed DOM may reference fonts, images or stylesheets on hosts
    // that are unreachable, slow, or long gone (measured: the probe capture's page
    // pulls a font from iana.org and `load` never fired). Evidence must render from
    // the file alone, so every non-file request is refused and readiness is the
    // page's own flag, not the browser's load event.
    await page.route("**/*", (route) =>
      route.request().url().startsWith("file:")
        ? route.continue()
        : route.abort(),
    );
    await page.goto(pathToFileURL(pageFile).href, {
      waitUntil: "domcontentloaded",
    });
    await page.waitForFunction(
      () =>
        (window as unknown as { __e2edocReady?: boolean }).__e2edocReady ===
        true,
      undefined,
      { timeout: options.timeoutMs ?? 15_000 },
    );
    // The player renders into an iframe asynchronously; give the seek a beat to paint.
    await page.waitForTimeout(300);

    const figures = page.locator("figure.player[data-ready='1']");
    const count = await figures.count();
    const frames: RenderedFrame[] = [];
    for (let i = 0; i < count; i++) {
      const figure = figures.nth(i);
      const name =
        (await figure.getAttribute("data-frame")) ?? `frame-${i + 1}`;
      const mount = figure.locator(".mount");
      // A player whose iframe holds no document body rendered nothing — that is a
      // segment that looks like evidence and shows nothing. Refuse it.
      const rendered = await mount.evaluate((el) => {
        const iframe = el.querySelector("iframe") as HTMLIFrameElement | null;
        const doc = iframe?.contentDocument;
        return Boolean(doc && doc.body && doc.body.childNodes.length > 0);
      });
      if (!rendered) {
        throw new Error(
          `frame "${name}" rendered an empty document — the segment is not reviewable`,
        );
      }
      const file = join(outDir, `${name}.png`);
      await mount.screenshot({ path: file });
      frames.push({ name, file });
    }
    return frames;
  } finally {
    await browser.close();
  }
}

/**
 * In-page rrweb recording for backends that do not record on their own.
 *
 * Solari records server-side and hands back an rrweb NDJSON stream (measured in the recording-addressability probe).
 * A local browser has no such service, so this injects rrweb's recorder into every
 * document the context loads and collects the same event shape — which is what lets
 * the evidence slicer treat both backends identically, and what stops Solari being
 * load-bearing for evidence (the driver-abstraction constraint: Solari is one backend among peers).
 *
 * Timestamps come from the page's own Date.now(). For a local browser that is the same
 * clock as the harness process, so step anchors and events share a frame exactly.
 */
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import type { BrowserContext } from "playwright-core";
import type { ReplayEvent } from "../evidence/types.mts";
import { CLASSIFY_HOOK, classifierBundleSource } from "../capture/events.mts";

// The package's `exports` map does not expose the UMD build, so resolve the main
// entry (dist/record.cjs) and walk to the sibling umd/ directory.
const require = createRequire(import.meta.url);
const RECORDER_UMD = readFileSync(
  join(dirname(require.resolve("@rrweb/record")), "..", "umd", "record.min.js"),
  "utf8",
);

const BINDING = "__e2edocReplayEvent";

/**
 * Recording starts at DOMContentLoaded, not document-start: the FullSnapshot then
 * captures a populated DOM instead of an empty one followed by a mutation storm, which
 * is both smaller and the shape the slicer's Meta→FullSnapshot invariant was measured
 * on. about:blank (the context's first page) is skipped — nothing to show.
 *
 * THE REPLAY STREAM IS A SECOND CHANNEL OUT OF THE PAGE, and it carries input values.
 * rrweb's own default masks passwords and nothing else, so an email address, a card
 * number or a home address typed during a recording used to cross a binding the capture
 * redaction never touched — and land in an evidence bundle attached to a pull request.
 *
 * So every input and every text node is routed through the recording's OWN
 * classification (capture/events.mts publishes it page-side) rather than through a
 * second policy: `maskAllInputs` is what makes rrweb call the function at all, and the
 * function returns the value UNCHANGED unless the classification says the field holds
 * personal or secret data, in which case it returns the same `<secret:category>`
 * placeholder the spec carries. Over-masking is not free either — a replay whose every
 * field reads `****` cannot be reviewed.
 *
 * With no recording on the page there is no classifier, and the stream must be no weaker
 * than what rrweb would have done alone: passwords stay masked.
 */
function bootstrap(): string {
  return `
(() => {
  if (location.href === "about:blank") return;
  const maskFor = (text, element) => {
    const classify = window.${CLASSIFY_HOOK};
    if (typeof classify === "function") {
      const found = element ? classify(element) : null;
      return found ? "<secret:" + found.category + ">" : text;
    }
    const type = element && element.tagName === "INPUT"
      ? (element.getAttribute("type") || "").toLowerCase()
      : "";
    return type === "password" ? "*".repeat(text.length) : text;
  };
  const start = () => {
    const api = window.rrwebRecord;
    if (!api || typeof api.record !== "function") return;
    api.record({
      emit: (event) => { window.${BINDING}(JSON.stringify(event)); },
      maskAllInputs: true,
      maskTextSelector: "*",
      maskInputFn: maskFor,
      maskTextFn: maskFor,
    });
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", start, { once: true });
  } else {
    start();
  }
})();`;
}

export interface RrwebRecorder {
  /** Events collected so far, sorted by timestamp. */
  events(): ReplayEvent[];
}

/** Must be called before the first page is created so the init script covers it. */
export async function attachRrwebRecorder(
  context: BrowserContext,
): Promise<RrwebRecorder> {
  const collected: ReplayEvent[] = [];
  await context.exposeBinding(BINDING, (_source, json: string) => {
    collected.push(JSON.parse(json) as ReplayEvent);
  });
  // The leading `;` is load-bearing: the UMD's last statement has no terminator, and
  // a following `(` would otherwise be parsed as a call on it (measured: the page threw
  // "(intermediate value)(...) is not a function" and recorded nothing).
  // The classifier goes in with the recorder, on the CONTEXT, so EVERY session gets it —
  // a replay as much as a recording. A replay installs rrweb and nothing else, and its
  // evidence bundle is what gets attached to a pull request; without this it fell back to
  // rrweb's own default and masked passwords alone while every other category the
  // recorder is careful about went in verbatim.
  await context.addInitScript({ content: classifierBundleSource() });
  await context.addInitScript({ content: `${RECORDER_UMD}\n;${bootstrap()}` });
  return {
    events: () => [...collected].sort((a, b) => a.timestamp - b.timestamp),
  };
}

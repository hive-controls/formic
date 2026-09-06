/**
 * The brief: what every healer backend is told, in one place.
 *
 * The same text is the system rules for an API-backed model and the HEAL.md a headless
 * agent reads, so the rules cannot drift between backends. Deliberately short — the
 * proposal parser enforces the rules structurally; this just explains them.
 */
import { saveSpec } from "../spec/parse.mts";
import type { HealContext } from "./types.mts";

export const HEALER_RULES = `You repair a broken end-to-end test spec. The spec is YAML data, not code.

You may propose EXACTLY ONE of:
- rewrite-target: change one step's locator/URL. The step keeps its id and its assert.
  This is also the ONLY way to change where the step is resolved: add "frame" beside
  "target" when the element moved into or out of an iframe.
- insert-step: add one step immediately before an existing step (a flow gained an
  interstitial, a confirmation, a dismissal). A click/fill/press/select step MUST carry
  an assert describing what the user sees after it.
- propose-assert-change: you believe the EXPECTATION is stale, not the locator. This
  is never applied automatically — a human accepts or rejects it in review.
- no-repair: you cannot repair it. Say why.

Rules:
1. Never change a step's assert. If the data on screen differs from what the spec
   expects, that may be a real bug in the application — use propose-assert-change.
2. Prefer the smallest change. A repair is reviewed as a one-line diff.
3. A step whose value reads \`valueFrom: env.NAME\` gets it from the environment at run
   time, deliberately: the spec is committed and reviewed, so the value is not in it.
   Carry that reference through untouched — never replace it with a literal \`value\`,
   and never copy a \`<secret:...>\` placeholder anywhere. An inserted step that needs
   such a value may use \`valueFrom\` itself.
4. A step's target locator: prefer data-testid, then stable ids, then role/text
   (\`role=button[name="Sign in"]\`, \`text=Approve\`). Never index-based selectors
   like :nth-child.
5. An assert is EITHER page-level — EXACTLY ONE of "url" (exact), "urlPrefix"
   (starts-with), "urlPattern" (regex) — OR element-level, never both and never more
   than one url key at once. An element assert has ONE locator — "testId", "selector",
   "role" (with an optional "name"), or "text" — plus any of "hasText", "containsText",
   "visible". Role and text follow Playwright: the name/text match is a substring by
   default; add "exact":true for a whole-string match. "name" is only valid alongside
   "role". The top-level key is "kind", never "type".
6. A step may carry a "frame": a list of frame references, OUTERMOST FIRST, each naming
   exactly one nested browsing context by "selector" (the owning <iframe> element, in the
   document that contains it), "name", "url" or "urlPrefix" (the frame's own document
   URL). No frame means the top-level page. An assert with no frame of its own is checked
   in its step's frame; give the assert a "frame" only when the consequence renders in a
   different document than the action. A url/urlPrefix/urlPattern assert is page-level
   and may NEVER carry a frame.
7. Base every proposal on the accessibility snapshot below — it is what the user sees
   right now, at the moment the step failed.

Reply with ONLY a JSON object of the proposal, no prose, no code fences:
  {"kind":"rewrite-target","stepId":"st_...","target":"...","reason":"..."}
  {"kind":"rewrite-target","stepId":"st_...","target":"...","frame":[{"selector":"#checkout"}],"reason":"..."}
  {"kind":"insert-step","beforeStepId":"st_...","step":{"action":"click","target":"...","assert":{"selector":"...","visible":true}},"reason":"..."}
  {"kind":"propose-assert-change","stepId":"st_...","to":{"role":"heading","name":"..."},"reason":"..."}
  {"kind":"no-repair","reason":"..."}`;

export function buildBrief(context: HealContext): string {
  const { failure, failedStep } = context;
  const prior =
    context.priorAttempts.length === 0
      ? ""
      : `\n## Earlier attempts this run (do not repeat them)\n${context.priorAttempts
          .map(
            (a, i) =>
              `${i + 1}. ${JSON.stringify(a.proposal)}\n   -> ${a.result.split("\n")[0]}`,
          )
          .join("\n")}\n`;

  return `# Heal request — attempt ${context.attempt}

## What broke
Step ${failure.index} (id \`${failedStep.id}\`) failed in the **${failure.phase}** phase.
- action: ${failedStep.action}
- target: ${failedStep.target ?? "(none)"}
${failedStep.frame !== undefined ? `- frame: ${JSON.stringify(failedStep.frame)}\n` : ""}${failedStep.value !== undefined ? `- value: ${failedStep.value}\n` : ""}${failedStep.valueFrom !== undefined ? `- valueFrom: ${failedStep.valueFrom}\n` : ""}${failedStep.assert ? `- assert: ${JSON.stringify(failedStep.assert)}\n` : ""}
Error:
\`\`\`
${failure.error.trim()}
\`\`\`
${prior}
## The page at the moment of failure
URL: ${context.url}

Accessibility snapshot (what the user sees):
\`\`\`
${context.ariaSnapshot.trim()}
\`\`\`

## The whole spec
\`\`\`yaml
${saveSpec(context.spec).trim()}
\`\`\`
`;
}

/**
 * Models wrap JSON in prose or fences despite instructions. Take the first balanced
 * object; anything else is a parse failure the caller reports as no proposal.
 */
export function extractJsonObject(text: string): unknown {
  const start = text.indexOf("{");
  if (start < 0) throw new Error("no JSON object in the response");
  let depth = 0;
  let inString = false;
  for (let i = start; i < text.length; i++) {
    const ch = text[i];
    if (inString) {
      if (ch === "\\") i++;
      else if (ch === '"') inString = false;
      continue;
    }
    if (ch === '"') inString = true;
    else if (ch === "{") depth++;
    else if (ch === "}") {
      depth--;
      if (depth === 0) return JSON.parse(text.slice(start, i + 1));
    }
  }
  throw new Error("unterminated JSON object in the response");
}

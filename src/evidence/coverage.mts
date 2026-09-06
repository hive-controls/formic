/**
 * Coverage from evidence, token-free: which interactive components the app showed during
 * a run that no spec step ever touched. Built on two deterministic fields of the audit
 * record — each step's end-state accessibility snapshot (`ariaSnapshot`) and the aria
 * line of the element each step acted on (`targetNode`) — so the output is a pure function
 * of the record. Nothing here is a model's guess.
 *
 * Output is a PROPOSAL, never applied: it lands in the evidence bundle (proposals.json),
 * the evidence page and the repair PR for a human to turn into a step, with the frame that
 * shows the component. Same rule as `proposedAssertChange`.
 */
import type { EvidenceRecord, StepRecord } from "./types.mts";

/** Roles a user acts on. Static roles (heading, text, list…) are never proposals. */
export const INTERACTIVE_ROLES: ReadonlySet<string> = new Set([
  "button",
  "link",
  "textbox",
  "searchbox",
  "combobox",
  "checkbox",
  "radio",
  "switch",
  "slider",
  "spinbutton",
  "menuitem",
  "tab",
  "option",
]);

export interface InteractiveNode {
  role: string;
  name: string;
}

export interface UntestedComponent extends InteractiveNode {
  /** The first step whose end state showed the component. Evidence is keyed by this. */
  firstSeenStepId: string;
  firstSeenIndex: number;
  /** A locator in the form the runner already accepts (the role= repair shape). */
  suggestedTarget: string;
  suggestedAction: "click" | "fill";
}

/** One `- role "name"` line, at any indentation, with or without a trailing colon. */
const NODE_LINE =
  /^\s*-\s+([a-z]+)\s+"((?:[^"\\]|\\.)*)"\s*(?:\[[^\]]*\])?\s*:?\s*$/;

export function parseInteractiveNodes(snapshot: string): InteractiveNode[] {
  const nodes: InteractiveNode[] = [];
  for (const line of snapshot.split("\n")) {
    const match = NODE_LINE.exec(line);
    if (!match || !INTERACTIVE_ROLES.has(match[1])) continue;
    nodes.push({ role: match[1], name: match[2].replace(/\\"/g, '"') });
  }
  return nodes;
}

const key = (node: InteractiveNode) => `${node.role} ${node.name}`;

function suggestedAction(role: string): UntestedComponent["suggestedAction"] {
  return role === "textbox" ||
    role === "searchbox" ||
    role === "combobox" ||
    role === "spinbutton"
    ? "fill"
    : "click";
}

export function suggestedTarget(node: InteractiveNode): string {
  return `role=${node.role}[name="${node.name.replace(/"/g, '\\"')}"]`;
}

function touchedKeys(steps: StepRecord[]): Set<string> {
  const touched = new Set<string>();
  for (const step of steps) {
    if (!step.targetNode) continue;
    for (const node of parseInteractiveNodes(step.targetNode))
      touched.add(key(node));
  }
  return touched;
}

/** Interactive components seen in any step's end state that no step acted on, in order
 *  of first appearance, deduplicated by role + accessible name. */
export function untestedComponents(
  record: EvidenceRecord,
): UntestedComponent[] {
  const touched = touchedKeys(record.steps);
  const seen = new Set<string>();
  const untested: UntestedComponent[] = [];
  for (const step of record.steps) {
    if (!step.ariaSnapshot) continue;
    for (const node of parseInteractiveNodes(step.ariaSnapshot)) {
      const k = key(node);
      if (touched.has(k) || seen.has(k)) continue;
      seen.add(k);
      untested.push({
        ...node,
        firstSeenStepId: step.id,
        firstSeenIndex: step.index,
        suggestedTarget: suggestedTarget(node),
        suggestedAction: suggestedAction(node.role),
      });
    }
  }
  return untested;
}

/** The YAML a human would paste into the spec for one proposal — a step with the
 *  action and target filled in and the assertion left for them to write. */
export function proposedStepYaml(component: UntestedComponent): string {
  const lines = [
    `- action: ${component.suggestedAction}`,
    `  target: '${component.suggestedTarget}'`,
  ];
  if (component.suggestedAction === "fill")
    lines.push("  value: <the value to type>");
  lines.push("  assert: <what a reviewer would SEE after this step>");
  return lines.join("\n");
}

export interface StepDrift {
  stepId: string;
  /** Display index in the CURRENT run. */
  index: number;
  added: InteractiveNode[];
  removed: InteractiveNode[];
}

export interface UiDrift {
  previousDecisionId: string;
  previousTimestamp: string;
  steps: StepDrift[];
}

/** Interactive components that appeared or disappeared in a step's end state since the
 *  previous record of the same spec, keyed by stable step id. Steps present in only one
 *  record (inserted or removed by a repair) are not drift — they have nothing to compare.
 *  Deterministic and token-free: the case a green replay is blind to (a new button no
 *  test touches) becomes a section, never a failure. */
export function uiDrift(
  previous: EvidenceRecord,
  current: EvidenceRecord,
): UiDrift {
  const before = new Map(
    previous.steps
      .filter((s) => s.ariaSnapshot)
      .map((s) => [s.id, parseInteractiveNodes(s.ariaSnapshot!)]),
  );
  const steps: StepDrift[] = [];
  for (const step of current.steps) {
    const prior = before.get(step.id);
    if (!prior || !step.ariaSnapshot) continue;
    const now = parseInteractiveNodes(step.ariaSnapshot);
    const priorKeys = new Set(prior.map(key));
    const nowKeys = new Set(now.map(key));
    const added = now.filter((n) => !priorKeys.has(key(n)));
    const removed = prior.filter((n) => !nowKeys.has(key(n)));
    if (added.length > 0 || removed.length > 0)
      steps.push({ stepId: step.id, index: step.index, added, removed });
  }
  return {
    previousDecisionId: previous.decisionId,
    previousTimestamp: previous.timestamp,
    steps,
  };
}

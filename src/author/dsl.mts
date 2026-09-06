/**
 * The authoring DSL: a compact YAML front onto the spec grammar — one step
 * per array entry, keyed by its action, so a step written by hand reads the same shape it
 * replays as. YAML rather than a second syntax, because the spec format is already YAML
 * and a separate grammar would be a second thing to learn for no gain.
 *
 * Deliberately thin: field-level assertion validation (types, blank locators, the
 * state-change rule) is spec/parse.mts's job, run again once ids and index are assigned —
 * this module owns only what a human can get wrong before that: an unknown or missing
 * action key, more than one action key on a step, an unknown sibling key, or a target that
 * will not parse. Every problem is reported with the source line it came from, because a
 * malformed step buried in a 40-line YAML doc with no line number is not "clear errors".
 */
import { LineCounter, isMap, isSeq, parseDocument } from "yaml";
import {
  ACTION_KINDS,
  type ActionKind,
  type Assertion,
} from "../spec/types.mts";

export class DslParseError extends Error {
  constructor(readonly problems: string[]) {
    super(`authoring input is invalid:\n  - ${problems.join("\n  - ")}`);
    this.name = "DslParseError";
  }
}

export interface DslStep {
  action: ActionKind;
  target: string;
  /** An explicit step id. Optional: authoring derives a stable one from the step's
   *  position when the input does not name it. Named here so a spec whose steps are
   *  referenced by id elsewhere can keep those ids across a rewrite of the input. */
  id?: string;
  value?: string;
  assert?: Assertion;
}

export interface DslDocument {
  name?: string;
  startUrl?: string;
  steps: DslStep[];
}

const ACTION_KEY_SET: ReadonlySet<string> = new Set(ACTION_KINDS);
const KNOWN_SIBLING_KEYS: ReadonlySet<string> = new Set([
  "id",
  "value",
  "assert",
]);

/** Parses the authoring DSL text into its raw fields — no id/index assignment, no spec
 *  validation. `authorSpec`/`extendSpec` in `./index.mts` build on this. */
export function parseDsl(text: string): DslDocument {
  const lineCounter = new LineCounter();
  const doc = parseDocument(text, { lineCounter });
  const problems: string[] = [];

  if (doc.errors.length > 0) {
    for (const error of doc.errors) {
      const line = lineCounter.linePos(error.pos[0]).line;
      problems.push(`line ${line}: ${error.message}`);
    }
    throw new DslParseError(problems);
  }

  const nameValue = doc.get("name");
  if (nameValue !== undefined && typeof nameValue !== "string") {
    problems.push("name must be a string");
  }
  const startUrlValue = doc.get("startUrl");
  if (startUrlValue !== undefined && typeof startUrlValue !== "string") {
    problems.push("startUrl must be a string");
  }

  const stepsNode = doc.get("steps", true);
  if (stepsNode === undefined || !isSeq(stepsNode)) {
    problems.push("steps must be a list");
    throw new DslParseError(problems);
  }
  if (stepsNode.items.length === 0) {
    problems.push("steps must be a non-empty list");
    throw new DslParseError(problems);
  }

  const steps: DslStep[] = [];
  stepsNode.items.forEach((itemNode, position) => {
    const line = lineCounter.linePos(
      (itemNode as { range?: [number, number, number] }).range?.[0] ??
        stepsNode.range?.[0] ??
        0,
    ).line;
    const at = `line ${line} (step ${position + 1})`;

    if (!isMap(itemNode)) {
      problems.push(`${at}: must be a mapping`);
      return;
    }

    const keys = itemNode.items.map((pair) => String(pair.key));
    const actionKeys = keys.filter((key) => ACTION_KEY_SET.has(key));
    if (actionKeys.length === 0) {
      problems.push(
        `${at}: missing an action key (one of ${ACTION_KINDS.join(" | ")})`,
      );
      return;
    }
    if (actionKeys.length > 1) {
      problems.push(
        `${at}: only one action key is allowed, got ${actionKeys.join(", ")}`,
      );
      return;
    }
    const unknownKeys = keys.filter(
      (key) => key !== actionKeys[0] && !KNOWN_SIBLING_KEYS.has(key),
    );
    if (unknownKeys.length > 0) {
      problems.push(`${at}: unknown field(s) ${unknownKeys.join(", ")}`);
    }

    const action = actionKeys[0] as ActionKind;
    const target = itemNode.get(action);
    if (typeof target !== "string" || target.trim() === "") {
      problems.push(`${at}: "${action}" needs a non-empty target`);
      return;
    }

    const value = itemNode.get("value");
    if (value !== undefined && typeof value !== "string") {
      problems.push(`${at}: value must be a string`);
    }

    const explicitId = itemNode.get("id");
    if (
      explicitId !== undefined &&
      (typeof explicitId !== "string" || explicitId.trim() === "")
    ) {
      problems.push(`${at}: id must be a non-empty string`);
    }

    const assertNode = itemNode.get("assert", true);
    let assertion: Assertion | undefined;
    if (assertNode !== undefined) {
      if (!isMap(assertNode)) {
        problems.push(`${at}: assert must be a mapping`);
      } else {
        assertion = assertNode.toJSON() as Assertion;
      }
    }

    const dslStep: DslStep = { action, target };
    if (typeof explicitId === "string" && explicitId.trim() !== "")
      dslStep.id = explicitId;
    if (typeof value === "string") dslStep.value = value;
    if (assertion !== undefined) dslStep.assert = assertion;
    steps.push(dslStep);
  });

  if (problems.length > 0) throw new DslParseError(problems);

  return {
    name: typeof nameValue === "string" ? nameValue : undefined,
    startUrl: typeof startUrlValue === "string" ? startUrlValue : undefined,
    steps,
  };
}

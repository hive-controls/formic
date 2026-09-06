/**
 * Export to code — the Deliverator's second delivery.
 *
 * A spec is data, and a team already invested in Puppeteer or Cypress should get value
 * from a capture without letting this harness drive their browser. So the spec stays
 * the source of truth and the exported test is a BUILD ARTIFACT: regenerate it, never
 * hand-edit it. That is why every generated file carries its provenance in the first
 * two lines — a hand-edit that drifts from the spec has to be visible.
 *
 * `compileSpec` either produces the whole test or refuses (`UnsupportedConstructError`)
 * naming the construct the target cannot express. There is no partial export: a test
 * that quietly lost an assertion is green in the consumer's CI and worse than none.
 */
import { join } from "node:path";
import { validateSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import {
  compile,
  UnsupportedConstructError,
  type Target,
} from "./compiler.mts";
import { TARGET_NAMES, type TargetName } from "./locators.mts";
import { playwrightTarget } from "./targets/playwright.mts";
import { puppeteerTarget } from "./targets/puppeteer.mts";
import { cypressTarget } from "./targets/cypress.mts";

export { UnsupportedConstructError } from "./compiler.mts";
export { TARGET_NAMES, type TargetName } from "./locators.mts";

const TARGETS: Record<TargetName, Target> = {
  playwright: playwrightTarget,
  puppeteer: puppeteerTarget,
  cypress: cypressTarget,
};

export interface CompileOptions {
  /** What the header names as the source. Defaults to the spec name. */
  specPath?: string;
  /** ISO date for the header. Defaults to today — pin it to compare output. */
  generatedOn?: string;
  /** Where the suggested `path` should sit. Defaults to the bare file name. */
  outDir?: string;
}

export interface CompiledExport {
  /** Suggested file name for the source, extension chosen by the target. */
  path?: string;
  source: string;
}

export function isTargetName(candidate: string): candidate is TargetName {
  return (TARGET_NAMES as readonly string[]).includes(candidate);
}

/** File-name-safe form of a spec name; specs are named by humans. */
function fileNameFor(spec: Spec, target: Target): string {
  const stem =
    spec.name
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, "-")
      .replace(/^-+|-+$/g, "") || "spec";
  return `${stem}${target.extension}`;
}

export function compileSpec(
  spec: Spec,
  target: TargetName,
  options: CompileOptions = {},
): CompiledExport {
  // `Spec` is a structural type, so a caller can hand this function an object TypeScript
  // accepts but the format forbids — a `click` with no assertion, say. The CLI validates
  // on load and validating again here is cheap; skipping it would let the library emit a
  // test the parser would have refused.
  validateSpec(spec);
  const emitter = TARGETS[target];
  if (emitter === undefined) {
    throw new UnsupportedConstructError(
      target,
      `the target \`${target}\``,
      `known targets are ${TARGET_NAMES.join(", ")}`,
    );
  }
  const fileName = fileNameFor(spec, emitter);
  return {
    path: options.outDir ? join(options.outDir, fileName) : fileName,
    source: compile(
      spec,
      emitter,
      options.specPath ?? spec.name,
      options.generatedOn ?? new Date().toISOString().slice(0, 10),
    ),
  };
}

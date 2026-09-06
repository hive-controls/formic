/**
 * The evidence bundle: everything a reviewer needs, as files in one directory.
 *
 *   index.html          the self-contained evidence page (players inlined)
 *   frames/*.png        one frame per player, rendered by a real browser
 *   initial.json        the failing (or passing) run's audit record
 *   attempt-N.json      proposal, BEFORE/AFTER segments, verification record
 *   proposals.json      untested components (proposed steps, never applied) and the
 *                       UI drift since the previous record of this spec, when one exists
 *
 * A bundle is what the repair PR commits next to the repaired spec, and what a CI job
 * uploads as an artifact. Writing it is deterministic given the heal result.
 */
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import {
  uiDrift,
  untestedComponents,
  type UiDrift,
  type UntestedComponent,
} from "../evidence/coverage.mts";
import type { EvidenceRecord } from "../evidence/types.mts";
import { renderFrames, type RenderedFrame } from "../evidence/frames.mts";
import { renderEvidencePage } from "../evidence/page.mts";
import { coverageRecord, renderSpecDiff } from "./cli.mts";
import type { HealResult } from "./loop.mts";
import type { Spec } from "../spec/types.mts";

export interface EvidenceBundle {
  dir: string;
  pageFile: string;
  frames: RenderedFrame[];
  /** Bundle-relative paths of every file written. */
  files: string[];
  diff: string[];
  /** Interactive components the green run showed that no step touched. */
  untested: UntestedComponent[];
  /** Drift since the previous record, when the caller supplied one. */
  drift: UiDrift | null;
}

export async function writeEvidenceBundle(
  original: Spec,
  result: HealResult,
  dir: string,
  options: {
    renderFrames?: boolean;
    /** The previous audit record of this spec (see heal/previous-record.mts). */
    previous?: EvidenceRecord;
  } = {},
): Promise<EvidenceBundle> {
  mkdirSync(dir, { recursive: true });
  // Generated wholesale: a frames directory from an earlier write must not survive, or a
  // render that fails part-way leaves old frames beside new JSON (seen on a Solari run).
  rmSync(join(dir, "frames"), { recursive: true, force: true });
  const diff = renderSpecDiff(original, result.spec);
  const green = coverageRecord(result);
  const untested = untestedComponents(green);
  const drift = options.previous ? uiDrift(options.previous, green) : null;
  // Already scrubbed to a reference and free of backend identity — every heal run
  // goes through assembleEvidence/scrubHealResult before it ever reaches a bundle.
  const initialEvidence = result.initial.evidence;
  const files: string[] = [];
  const write = (name: string, content: string) => {
    writeFileSync(join(dir, name), content);
    files.push(name);
  };

  write("initial.json", JSON.stringify(initialEvidence, null, 2));
  result.attempts.forEach((attempt, i) =>
    write(
      `attempt-${i + 1}.json`,
      JSON.stringify(
        {
          proposal: attempt.proposal,
          before: attempt.before,
          after: attempt.after,
          verification: attempt.verification
            ? attempt.verification.evidence
            : null,
        },
        null,
        2,
      ),
    ),
  );
  write(
    "proposals.json",
    JSON.stringify({ untestedComponents: untested, uiDrift: drift }, null, 2),
  );
  const pageFile = join(dir, "index.html");
  write(
    "index.html",
    renderEvidencePage({
      specName: result.spec.name,
      outcome: result.outcome,
      initial: initialEvidence,
      healer: result.attempts.length > 0 ? result.healer : undefined,
      diff,
      untested,
      drift: drift ?? undefined,
      attempts: result.attempts.map((a) => ({
        attempt: a.attempt,
        proposal: a.proposal,
        before: a.before,
        after: a.after,
        verification: a.verification ? a.verification.evidence : null,
      })),
    }),
  );

  let frames: RenderedFrame[] = [];
  if (options.renderFrames ?? true) {
    frames = await renderFrames(pageFile, join(dir, "frames"));
    for (const frame of frames) files.push(`frames/${frame.name}.png`);
  }
  return { dir, pageFile, frames, files, diff, untested, drift };
}

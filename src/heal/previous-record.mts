/**
 * The previous audit record for a spec: the newest `initial.json` under the evidence
 * root (the directory repair PRs commit bundles to — `evidenceDirFor` without the
 * decision segment) whose `specName` matches. What the UI-drift diff compares against.
 * Read-only, best-effort: a missing root, an unreadable bundle or a foreign spec's
 * bundle are skipped, never errors.
 */
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import type { EvidenceRecord } from "../evidence/types.mts";

function readRecord(file: string): EvidenceRecord | undefined {
  try {
    const parsed = JSON.parse(readFileSync(file, "utf8")) as EvidenceRecord;
    return typeof parsed?.decisionId === "string" &&
      typeof parsed?.timestamp === "string" &&
      Array.isArray(parsed?.steps)
      ? parsed
      : undefined;
  } catch {
    return undefined;
  }
}

export function findPreviousRecord(
  evidenceRoot: string,
  specName: string,
  options: { excludeDecisionId?: string } = {},
): EvidenceRecord | undefined {
  if (!existsSync(evidenceRoot)) return undefined;
  let newest: EvidenceRecord | undefined;
  for (const entry of readdirSync(evidenceRoot)) {
    const dir = join(evidenceRoot, entry);
    try {
      if (!statSync(dir).isDirectory()) continue;
    } catch {
      continue;
    }
    const record = readRecord(join(dir, "initial.json"));
    if (!record || record.specName !== specName) continue;
    if (record.decisionId === options.excludeDecisionId) continue;
    if (!newest || record.timestamp > newest.timestamp) newest = record;
  }
  return newest;
}

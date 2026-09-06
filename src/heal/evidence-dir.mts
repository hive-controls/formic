/**
 * Where a repair PR commits its evidence bundle, as a repo-relative posix path.
 *
 * Derived from the spec so an adopter needs no configuration: a spec kept under a
 * `specs/` directory gets `evidence/` beside that directory (`specs/a.yaml` →
 * `evidence/<decision>`); any other spec gets `evidence/` next to the file. An explicit
 * override (`FORMIC_EVIDENCE_DIR`, a repo-relative directory) wins; it is normalized and
 * must stay inside the repository. The decision id is always the last segment, so one
 * evidence directory holds every repair's bundle side by side.
 */
import { posix } from "node:path";

function normalizeRepoRelative(dir: string, what: string): string {
  const normalized = posix.normalize(dir.replace(/\\/g, "/"));
  if (
    posix.isAbsolute(normalized) ||
    normalized === ".." ||
    normalized.startsWith("../")
  ) {
    throw new Error(
      `${what} must be a directory inside the repository, got "${dir}"`,
    );
  }
  return normalized === "." ? "" : normalized.replace(/\/$/, "");
}

export function evidenceDirFor(
  specFile: string,
  decisionId: string,
  override?: string,
): string {
  if (override && override.trim() !== "") {
    const base = normalizeRepoRelative(override.trim(), "FORMIC_EVIDENCE_DIR");
    return base ? posix.join(base, decisionId) : decisionId;
  }
  const specDir = posix.dirname(specFile.replace(/\\/g, "/"));
  const anchor =
    posix.basename(specDir) === "specs" ? posix.dirname(specDir) : specDir;
  const base = anchor === "." ? "" : anchor;
  return posix.join(base, "evidence", decisionId);
}

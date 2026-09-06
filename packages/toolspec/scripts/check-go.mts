import { readFileSync, rmSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  GO_ENV,
  describeGoFailure,
  goInstallMessage,
  requireGo,
} from "./go-env.mts";

const PACKAGE_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const GO_DIR = join(PACKAGE_ROOT, "go");
const GENERATED_PATH = join(GO_DIR, "toolspec.gen.go");
const CANONICAL_SCHEMA_PATH = join(
  PACKAGE_ROOT,
  "schema",
  "toolspec.schema.json",
);
const EMBEDDED_SCHEMA_PATH = join(GO_DIR, "toolspec.schema.json");

function main(): void {
  requireGo(goInstallMessage("the toolspec Go drift check"));

  if (
    readFileSync(EMBEDDED_SCHEMA_PATH, "utf8") !==
    readFileSync(CANONICAL_SCHEMA_PATH, "utf8")
  ) {
    console.error(
      `Drift detected: ${EMBEDDED_SCHEMA_PATH} does not match ${CANONICAL_SCHEMA_PATH}.\n` +
        "Copy the canonical schema into the Go module and commit both files.",
    );
    process.exit(1);
  }

  // Run the real `go generate ./...` directive (packages/toolspec/go/generate.go) rather than
  // re-typing its go-jsonschema flags here — one source of truth for the generator invocation,
  // so a flag change in generate.go can never go unnoticed by this check.
  //
  // Delete the committed file FIRST. `go generate` exits 0 even when there is nothing to run
  // (a missing, commented-out, or otherwise neutralized go:generate directive) — leaving the
  // old file in place would then compare byte-equal to itself and silently pass. Deleting
  // first makes "no output" indistinguishable from "drift": the post-generate read fails and
  // the check goes red. The committed content is restored on every path (success, failure, or
  // a thrown error), so this check never leaves the working tree mutated.
  const committed = readFileSync(GENERATED_PATH, "utf8");
  let regenerated: string | null = null;
  const result = (() => {
    try {
      rmSync(GENERATED_PATH);
      const spawned = spawnSync("go", ["generate", "./..."], {
        cwd: GO_DIR,
        encoding: "utf8",
        env: GO_ENV,
      });
      try {
        regenerated = readFileSync(GENERATED_PATH, "utf8");
      } catch {
        regenerated = null;
      }
      return spawned;
    } finally {
      writeFileSync(GENERATED_PATH, committed);
    }
  })();

  if (result.error || result.status !== 0) {
    console.error(
      describeGoFailure(
        'Failed to run "go generate ./..." for the drift check',
        result.stderr ?? "",
        result.error,
      ),
    );
    process.exit(1);
  }

  if (regenerated === null || regenerated !== committed) {
    console.error(
      `Drift detected: ${GENERATED_PATH} does not match schema/toolspec.schema.json ` +
        `(or go:generate produced no output at all).\n` +
        `Run "go generate ./..." in packages/toolspec/go and commit the result.`,
    );
    process.exit(1);
  }
}

main();

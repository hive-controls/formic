import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  COLD_CACHE_OFFLINE_MESSAGE,
  GO_ENV,
  goInstallMessage,
  looksOffline,
  requireGo,
} from "./go-env.mts";

const PACKAGE_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..");
const GO_DIR = join(PACKAGE_ROOT, "go");

function main(): void {
  requireGo(goInstallMessage("the toolspec Go conformance tests"));

  // -count=1 disables Go's test result cache. The conformance tests read
  // fixtures/conformance/*.json and manifest.md at runtime via relative-path os.ReadFile —
  // files Go's cache does not track as build inputs — so a fixture-only change (edit,
  // delete, add) between runs would otherwise replay a stale cached "ok" instead of
  // re-executing.
  const result = spawnSync("go", ["test", "-count=1", "./..."], {
    cwd: GO_DIR,
    encoding: "utf8",
    env: GO_ENV,
  });
  process.stdout.write(result.stdout ?? "");
  process.stderr.write(result.stderr ?? "");

  if (result.error) {
    console.error(`Failed to run "go test ./...": ${result.error}`);
    process.exit(1);
  }
  if (result.status !== 0) {
    if (looksOffline(result.stderr ?? "")) {
      console.error(COLD_CACHE_OFFLINE_MESSAGE);
    }
    process.exit(result.status ?? 1);
  }
}

main();

import { readFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Pin the toolchain and forbid go.mod/go.sum rewrites, so neither the drift check nor the
// test wiring can silently mutate the module graph or fetch a different Go version than
// what's installed. A cold module cache still needs network for its FIRST download of a
// pinned dependency — that stays documented, expected behavior; these scripts only make
// sure a run never rewrites anything or reaches past the local toolchain to get there.
export const GO_ENV: NodeJS.ProcessEnv = {
  ...process.env,
  GOFLAGS: "-mod=readonly",
  GOTOOLCHAIN: "local",
};

const OFFLINE_PATTERN =
  /dial tcp|lookup .*(no such host|server misbehaving)|i\/o timeout|network is unreachable|TLS handshake timeout|connection refused/i;

export function requireGo(installMessage: string): void {
  const probe = spawnSync("go", ["version"], { stdio: "ignore" });
  if (probe.error || probe.status !== 0) {
    console.error(installMessage);
    process.exit(1);
  }
}

const GO_MOD_PATH = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "go",
  "go.mod",
);
const GO_DIRECTIVE_PATTERN = /^go\s+(\d+\.\d+(?:\.\d+)?)\s*$/m;

// Reads the minimum Go version straight from go/go.mod's `go` directive, so the install
// message never drifts from what GOTOOLCHAIN=local (which forbids auto-fetching a newer
// toolchain to satisfy it) actually requires.
function requiredGoVersion(): string {
  try {
    const match = GO_DIRECTIVE_PATTERN.exec(readFileSync(GO_MOD_PATH, "utf8"));
    if (match) {
      return match[1];
    }
  } catch {
    // fall through to the generic fallback below
  }
  return "the version pinned in packages/toolspec/go/go.mod";
}

export function goInstallMessage(context: string): string {
  return (
    `Go toolchain not found on PATH — ${context} requires it.\n` +
    `Install Go ${requiredGoVersion()} or newer, then re-run.`
  );
}

export function looksOffline(stderr: string): boolean {
  return OFFLINE_PATTERN.test(stderr);
}

export const COLD_CACHE_OFFLINE_MESSAGE =
  "The local Go module cache is cold and the network is unreachable.\n" +
  "Run this once with network access to populate the cache; it works offline after that.";

// Renders a clear, distinguishing message when a go subprocess fails because the module
// cache is cold and the network is unreachable — GOFLAGS=-mod=readonly turns what would
// otherwise be a silent go.mod rewrite into a loud failure here instead.
export function describeGoFailure(
  context: string,
  stderr: string,
  error?: Error,
): string {
  if (looksOffline(stderr)) {
    return `${context}: ${COLD_CACHE_OFFLINE_MESSAGE}\n${stderr}`;
  }
  return `${context}: ${stderr || error}`;
}

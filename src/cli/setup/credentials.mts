/**
 * The gitignored half of the Cocoon — Launchie's `.env` writer. Never logs a value:
 * every export here returns metadata (file path, action taken) or a boolean, never
 * the secret itself.
 *
 * `upsertEnvVar` also sets `process.env[name]` directly, because `dotenv/config`
 * already ran at CLI-import time (e2e-doctor.mts:32) and would never re-read a file
 * this process just wrote — the in-process value has to be set by hand.
 *
 * `envIsIgnored` is conservative on purpose: it looks for an exact `.gitignore` line
 * of `.env`, `.env*`, or `*.env` (no full gitignore pattern engine), walking from
 * `cwd` up to the git toplevel — a pattern anywhere on that path covers `cwd/.env`.
 */
import { execFileSync } from "node:child_process";
import { chmodSync, existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

const ENV_IGNORE_LINES = new Set([".env", ".env*", "*.env"]);

export function gitToplevel(cwd: string): string | null {
  try {
    const out = execFileSync("git", ["rev-parse", "--show-toplevel"], {
      cwd,
      stdio: ["ignore", "pipe", "ignore"],
    });
    return out.toString().trim();
  } catch {
    return null;
  }
}

function gitignoreCoversEnv(gitignorePath: string): boolean {
  if (!existsSync(gitignorePath)) return false;
  return readFileSync(gitignorePath, "utf8")
    .split("\n")
    .some((line) => ENV_IGNORE_LINES.has(line.trim()));
}

export function envIsIgnored(cwd: string): boolean {
  const toplevel = gitToplevel(cwd);
  let dir = cwd;
  for (;;) {
    if (gitignoreCoversEnv(join(dir, ".gitignore"))) return true;
    if (toplevel && dir === toplevel) return false;
    const parent = dirname(dir);
    if (parent === dir) return false;
    dir = parent;
  }
}

function replaceOrAppend(
  original: string,
  name: string,
  line: string,
): { content: string; action: "replaced" | "appended" } {
  const pattern = new RegExp(`^${name}=.*$`);
  const lines = original.split("\n");
  let replaced = false;
  const next = lines.map((existing) => {
    if (!replaced && pattern.test(existing)) {
      replaced = true;
      return line;
    }
    return existing;
  });
  if (replaced) return { content: next.join("\n"), action: "replaced" };
  const body =
    original.endsWith("\n") || original === "" ? original : `${original}\n`;
  return { content: `${body}${line}\n`, action: "appended" };
}

export function upsertEnvVar(
  cwd: string,
  name: string,
  value: string,
): { file: string; action: "created" | "replaced" | "appended" } {
  const file = join(cwd, ".env");
  const line = `${name}=${value}`;
  let action: "created" | "replaced" | "appended";
  if (!existsSync(file)) {
    writeFileSync(file, `${line}\n`);
    action = "created";
  } else {
    const result = replaceOrAppend(readFileSync(file, "utf8"), name, line);
    writeFileSync(file, result.content);
    action = result.action;
  }
  chmodSync(file, 0o600);
  process.env[name] = value;
  return { file, action };
}

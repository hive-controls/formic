/**
 * The environment one `formic run` hands to a recipe.
 *
 * Two rules make this safe to read and safe to run:
 *
 *   - ONLY KEYS THE MANIFEST DECLARES. A recipe's `env[]` is the contract; a key it
 *     never declared is a key nobody documented, and setting one would be the
 *     configurator inventing product surface. A resolved choice whose key the manifest
 *     does not declare is DROPPED and named out loud — never dropped silently, which
 *     is how a cloud gate turns into an unannounced local run.
 *   - SECRETS BY NAME, NEVER BY VALUE. A secret is read from the ambient environment or
 *     a local `.env` and passed straight through; the printed line carries the name and
 *     `***`, and the value never reaches a log.
 *
 * Defaults declared in the manifest are deliberately NOT applied here. A default is the
 * recipe's own, documented for a launcher's benefit; re-sending it as an explicit value
 * would pin today's default into every run and hide the day the recipe changes it.
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import type { ToolManifest } from "./types.mts";

/** Environment variables owned by someone else that always pass through when set. */
export const PASSTHROUGH_KEYS = ["HIVEDECK_STATUS_FILE"];

export interface BuildRunEnvInput {
  manifest: ToolManifest;
  /** Keys the configurator resolved from gate/healer/host/profile choices. */
  resolved: Record<string, string>;
  /** Of those, the ones whose values must never be printed. */
  resolvedSecretKeys?: string[];
  ambient: NodeJS.ProcessEnv;
  /** Values read from a local `.env`; ambient wins over these. */
  dotenv?: Record<string, string>;
}

export interface RunEnv {
  /** What is added on top of the ambient environment for the child process. */
  values: Record<string, string>;
  /** One redacted line per declared variable that got a value. */
  lines: string[];
  /** Resolved keys the manifest does not declare, so nothing is dropped in silence. */
  dropped: string[];
}

/** A tiny `KEY=value` reader. Enough for a `.env` a wizard wrote: no interpolation, no
 *  `export` prefixes, no multi-line values — shapes this file deliberately does not
 *  invent support for, because a value it guessed wrong at would be a silent
 *  misconfiguration rather than a refusal. */
export function parseDotenv(text: string): Record<string, string> {
  const values: Record<string, string> = {};
  for (const rawLine of text.split("\n")) {
    const line = rawLine.trim();
    if (line === "" || line.startsWith("#")) continue;
    const equals = line.indexOf("=");
    if (equals <= 0) continue;
    const name = line.slice(0, equals).trim();
    const value = line.slice(equals + 1).trim();
    values[name] = value.replace(/^(['"])(.*)\1$/, "$2");
  }
  return values;
}

export function loadDotenv(cwd: string): Record<string, string> {
  const path = join(cwd, ".env");
  return existsSync(path) ? parseDotenv(readFileSync(path, "utf8")) : {};
}

export function buildRunEnv(input: BuildRunEnvInput): RunEnv {
  const declared = input.manifest.env ?? [];
  const dotenv = input.dotenv ?? {};
  const resolvedSecrets = new Set(input.resolvedSecretKeys ?? []);
  const values: Record<string, string> = {};
  const lines: string[] = [];

  for (const variable of declared) {
    const name = variable.name;
    const value = input.resolved[name] ?? input.ambient[name] ?? dotenv[name];
    if (value === undefined || value === "") continue;
    values[name] = value;
    const secret = variable.secret === true || resolvedSecrets.has(name);
    lines.push(`${name}=${secret ? "***" : value}`);
  }

  for (const name of PASSTHROUGH_KEYS) {
    const value = input.ambient[name];
    if (value) values[name] = value;
  }

  const declaredNames = new Set(declared.map((variable) => variable.name));
  const dropped = Object.keys(input.resolved).filter(
    (name) => !declaredNames.has(name),
  );
  return { values, lines, dropped };
}

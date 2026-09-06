/**
 * Healer profiles — a saved shortcut for `--healer <name>`: which backend (agent or
 * API), which preset/model, and where the credential lives — never the credential
 * itself. `formic.profiles.yaml` is meant to be committed (Launchie will write it), so
 * the rule this module exists to enforce is one line: a profile may name the ENV
 * VARIABLE that carries a secret (`apiKeyFrom: env.NAME`), never the secret.
 *
 * `PRESETS.verified` is false on every entry: no preset here has been exercised by a
 * live smoke heal from this module (⚠️ ASSUMED, per the repo's shipped-artifact rule —
 * a live run is what would flip it to true).
 */
import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { parse as parseYaml, stringify as stringifyYaml } from "yaml";
import { AGENT_PRESETS as ADAPTERS } from "./agent-presets.mts";

export const PROFILES_FILE = "formic.profiles.yaml";

export type PresetName =
  | "anthropic"
  | "openrouter"
  | "openai"
  | "ollama"
  | "lmstudio"
  | "llamacpp"
  | "vllm"
  | "custom";

export interface Preset {
  baseUrl: string | null;
  defaultModel: string | null;
  envKeyName: string | null;
  verified: boolean;
  note?: string;
}

export const PRESETS: Record<PresetName, Preset> = {
  anthropic: {
    baseUrl: "https://api.anthropic.com/v1",
    defaultModel: "claude-sonnet-5",
    envKeyName: "ANTHROPIC_API_KEY",
    verified: false,
    note: "⚠️ ASSUMED — never run live by this module (from-env.mts's own default).",
  },
  openrouter: {
    baseUrl: "https://openrouter.ai/api/v1",
    defaultModel: null,
    envKeyName: "OPENROUTER_API_KEY",
    verified: false,
  },
  openai: {
    baseUrl: "https://api.openai.com/v1",
    defaultModel: null,
    envKeyName: "OPENAI_API_KEY",
    verified: false,
  },
  ollama: {
    baseUrl: "http://127.0.0.1:11434/v1",
    defaultModel: null,
    envKeyName: null,
    verified: false,
  },
  lmstudio: {
    baseUrl: "http://127.0.0.1:1234/v1",
    defaultModel: null,
    envKeyName: null,
    verified: false,
  },
  llamacpp: {
    // llama.cpp's `llama-server` speaks the OpenAI chat-completions API on :8080.
    baseUrl: "http://127.0.0.1:8080/v1",
    defaultModel: null,
    envKeyName: null,
    verified: false,
  },
  vllm: {
    baseUrl: "http://127.0.0.1:8000/v1",
    defaultModel: null,
    envKeyName: null,
    verified: false,
  },
  custom: {
    baseUrl: null,
    defaultModel: null,
    envKeyName: null,
    verified: false,
  },
};

export interface AgentProfile {
  kind: "agent";
  agent: string;
  command?: string;
  timeoutMs?: number;
  /** Passed to the adapter via modelArgs/modelEnv when it declares a route (agent-cli.mts);
   *  shape-validated only, never checked against a model list — see checkAgentProfile. */
  model?: string;
}

export interface ApiProfile {
  kind: "api";
  preset: PresetName;
  baseUrl?: string;
  model: string;
  apiKeyFrom?: string;
  timeoutMs?: number;
}

export type HealerProfile = AgentProfile | ApiProfile;

export interface ProfilesFile {
  default?: string;
  profiles: Record<string, HealerProfile>;
}

export class ProfileValidationError extends Error {
  constructor(readonly problems: string[]) {
    super(`profiles file is invalid:\n  - ${problems.join("\n  - ")}`);
    this.name = "ProfileValidationError";
  }
}

const API_KEY_FROM_PATTERN = /^env\.[A-Za-z_][A-Za-z0-9_]*$/;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function nonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "";
}

function checkTimeoutMs(
  name: string,
  value: unknown,
  problems: string[],
): void {
  if (value !== undefined && !(typeof value === "number" && value > 0)) {
    problems.push(`profile "${name}": timeoutMs must be a positive number`);
  }
}

function checkAgentProfile(
  name: string,
  value: Record<string, unknown>,
  problems: string[],
): void {
  const known = [...Object.keys(ADAPTERS), "custom"];
  if (value.agent === "custom") {
    if (!nonEmptyString(value.command)) {
      problems.push(`profile "${name}": agent: custom requires a command`);
    }
  } else if (!nonEmptyString(value.agent) || !(value.agent in ADAPTERS)) {
    problems.push(
      `profile "${name}": unknown agent "${String(value.agent)}" (expected ${known.join(" | ")})`,
    );
  }
  checkTimeoutMs(name, value.timeoutMs, problems);
  if (value.model !== undefined && !nonEmptyString(value.model)) {
    problems.push(`profile "${name}": model must be a non-empty string`);
  }
}

function checkApiProfile(
  name: string,
  value: Record<string, unknown>,
  problems: string[],
): void {
  if ("apiKey" in value) {
    problems.push(
      `profile "${name}": the file is committed; use apiKeyFrom: env.NAME instead of apiKey`,
    );
  }
  const presetName = value.preset;
  if (!nonEmptyString(presetName) || !(presetName in PRESETS)) {
    problems.push(
      `profile "${name}": unknown preset "${String(presetName)}" (expected ${Object.keys(PRESETS).join(" | ")})`,
    );
  } else if (presetName === "custom" && !nonEmptyString(value.baseUrl)) {
    problems.push(`profile "${name}": preset: custom requires a baseUrl`);
  }
  if (!nonEmptyString(value.model)) {
    problems.push(`profile "${name}": model is required`);
  }
  if (
    value.apiKeyFrom !== undefined &&
    (!nonEmptyString(value.apiKeyFrom) ||
      !API_KEY_FROM_PATTERN.test(value.apiKeyFrom as string))
  ) {
    problems.push(
      `profile "${name}": apiKeyFrom must match env.<NAME>, got "${String(value.apiKeyFrom)}"`,
    );
  }
  checkTimeoutMs(name, value.timeoutMs, problems);
}

export function validateProfiles(
  candidate: unknown,
): asserts candidate is ProfilesFile {
  if (!isRecord(candidate) || !isRecord(candidate.profiles)) {
    throw new ProfileValidationError([
      "profiles file must be a mapping with a profiles: mapping of name -> profile",
    ]);
  }
  const problems: string[] = [];
  const names = Object.keys(candidate.profiles);
  for (const name of names) {
    const value = candidate.profiles[name];
    if (!isRecord(value)) {
      problems.push(`profile "${name}" must be a mapping`);
    } else if (value.kind === "agent") {
      checkAgentProfile(name, value, problems);
    } else if (value.kind === "api") {
      checkApiProfile(name, value, problems);
    } else {
      problems.push(
        `profile "${name}": kind must be "agent" or "api", got ${String(value.kind)}`,
      );
    }
  }
  if (
    candidate.default !== undefined &&
    (!nonEmptyString(candidate.default) ||
      !names.includes(candidate.default as string))
  ) {
    problems.push(
      `default names "${String(candidate.default)}" but no such profile exists`,
    );
  }
  if (problems.length > 0) throw new ProfileValidationError(problems);
}

export function parseProfiles(yamlText: string): ProfilesFile {
  const parsed: unknown = parseYaml(yamlText);
  validateProfiles(parsed);
  return parsed;
}

export function loadProfiles(cwd: string = process.cwd()): ProfilesFile | null {
  const path = join(cwd, PROFILES_FILE);
  if (!existsSync(path)) return null;
  return parseProfiles(readFileSync(path, "utf8"));
}

/** Returns YAML text; this module never writes to disk (setup.mts, Wave 2, does). */
export function saveProfiles(file: ProfilesFile): string {
  return stringifyYaml(file, null, { lineWidth: 0 });
}

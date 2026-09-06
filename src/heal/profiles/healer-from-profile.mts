/**
 * Turn a saved profile into a live Healer, delegating to the same two backends
 * `FORMIC_HEALER` already selects (openai-compatible / agent-cli) — a profile is a
 * named shortcut for their options, never a third implementation.
 */
import {
  ADAPTERS,
  adapterFromCommand,
  agentCliHealer,
} from "../healers/agent-cli.mts";
import { openAiCompatibleHealer } from "../healers/openai-compatible.mts";
import type { Healer } from "../types.mts";
import { PRESETS, type HealerProfile } from "./profiles.mts";

function agentHealerFromProfile(
  profile: Extract<HealerProfile, { kind: "agent" }>,
  name: string,
  env: NodeJS.ProcessEnv,
): Healer {
  const adapter =
    profile.agent === "custom"
      ? adapterFromCommand(profile.command ?? "")
      : ADAPTERS[profile.agent];
  if (!adapter) {
    const known = [...Object.keys(ADAPTERS), "custom"];
    throw new Error(
      `profile "${name}": unknown agent "${profile.agent}" (expected ${known.join(" | ")})`,
    );
  }
  return agentCliHealer({
    adapter,
    timeoutMs: profile.timeoutMs,
    env,
    model: profile.model,
  });
}

function apiHealerFromProfile(
  profile: Extract<HealerProfile, { kind: "api" }>,
  name: string,
  env: NodeJS.ProcessEnv,
): Healer {
  const preset = PRESETS[profile.preset];
  const baseUrl = profile.baseUrl ?? preset.baseUrl;
  if (!baseUrl) {
    throw new Error(
      `profile "${name}": preset "${profile.preset}" has no default baseUrl — set baseUrl on the profile`,
    );
  }
  let apiKey: string | undefined;
  if (profile.apiKeyFrom) {
    const varName = profile.apiKeyFrom.slice("env.".length);
    apiKey = env[varName];
    if (!apiKey) {
      throw new Error(
        `profile "${name}": ${varName} is not set (needed by apiKeyFrom: env.${varName})`,
      );
    }
  }
  return openAiCompatibleHealer({
    baseUrl,
    model: profile.model,
    apiKey,
    timeoutMs: profile.timeoutMs,
  });
}

export function healerFromProfile(
  profile: HealerProfile,
  name: string,
  env: NodeJS.ProcessEnv = process.env,
): Healer {
  return profile.kind === "agent"
    ? agentHealerFromProfile(profile, name, env)
    : apiHealerFromProfile(profile, name, env);
}

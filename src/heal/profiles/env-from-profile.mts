/**
 * Turn a saved profile into ENVIRONMENT — the configurator's whole job on the healer
 * axis. A profile is a named shortcut for the variables the recipe already reads
 * (`…_HEALER` and its options), never a third healer implementation and never a
 * process this package spawns.
 *
 * A named agent preset resolves through the recipe's GENERIC custom-command route
 * (`…_HEALER=agent:custom` + `…_HEALER_AGENT_CMD=<argv>`) rather than through
 * `agent:<name>`: the recipe ships exactly the one adapter it has exercised itself,
 * and vendor argv is platform data (agent-presets.mts). A model is substituted into
 * that argv here, because the recipe's env grammar carries no model to an agent —
 * the printed label and the audit row can only be true to argv that was passed.
 */
import { configKey } from "../../config/keys.mts";
import {
  AGENT_PRESETS,
  presetCommandTemplate,
  quoteCommandToken,
  type AgentPreset,
} from "./agent-presets.mts";
import { PRESETS, type HealerProfile } from "./profiles.mts";

export interface ResolvedEnv {
  values: Record<string, string>;
  /** Keys whose values must never be printed. */
  secretKeys: string[];
}

/** A custom profile's own command template, with any `{model}` token filled in. The
 *  recipe refuses a template naming `{model}` with no model, so an unfilled token
 *  would be a run that cannot start. */
function customCommand(command: string, model: string | undefined): string {
  if (!command.includes("{model}")) return command;
  if (!model) {
    throw new Error(
      `a custom agent command naming {model} needs model: <name> on the profile`,
    );
  }
  return command.replace(/\{model\}/g, quoteCommandToken(model));
}

function agentEnv(
  profile: Extract<HealerProfile, { kind: "agent" }>,
  name: string,
  prefix: string,
): ResolvedEnv {
  const values: Record<string, string> = {
    [configKey("HEALER", prefix)]: "agent:custom",
  };
  if (profile.agent === "custom") {
    values[configKey("HEALER_AGENT_CMD", prefix)] = customCommand(
      profile.command ?? "",
      profile.model,
    );
  } else {
    const preset: AgentPreset | undefined = AGENT_PRESETS[profile.agent];
    if (!preset) {
      const known = [...Object.keys(AGENT_PRESETS), "custom"];
      throw new Error(
        `profile "${name}": unknown agent "${profile.agent}" (expected ${known.join(" | ")})`,
      );
    }
    values[configKey("HEALER_AGENT_CMD", prefix)] = presetCommandTemplate(
      preset,
      profile.model,
    );
  }
  if (profile.timeoutMs) {
    values[configKey("HEALER_TIMEOUT_MS", prefix)] = String(profile.timeoutMs);
  }
  return { values, secretKeys: [] };
}

function apiEnv(
  profile: Extract<HealerProfile, { kind: "api" }>,
  name: string,
  env: NodeJS.ProcessEnv,
  prefix: string,
): ResolvedEnv {
  const baseUrl = profile.baseUrl ?? PRESETS[profile.preset].baseUrl;
  if (!baseUrl) {
    throw new Error(
      `profile "${name}": preset "${profile.preset}" has no default baseUrl — set baseUrl on the profile`,
    );
  }
  const values: Record<string, string> = {
    [configKey("HEALER", prefix)]: "openai-compatible",
    [configKey("HEALER_BASE_URL", prefix)]: baseUrl,
    [configKey("HEALER_MODEL", prefix)]: profile.model,
  };
  const secretKeys: string[] = [];
  if (profile.apiKeyFrom) {
    const sourceVar = profile.apiKeyFrom.slice("env.".length);
    const apiKey = env[sourceVar];
    if (!apiKey) {
      throw new Error(
        `profile "${name}": ${sourceVar} is not set (needed by apiKeyFrom: env.${sourceVar})`,
      );
    }
    const key = configKey("HEALER_API_KEY", prefix);
    values[key] = apiKey;
    secretKeys.push(key);
  }
  if (profile.timeoutMs) {
    values[configKey("HEALER_TIMEOUT_MS", prefix)] = String(profile.timeoutMs);
  }
  return { values, secretKeys };
}

export function envFromProfile(
  profile: HealerProfile,
  name: string,
  env: NodeJS.ProcessEnv,
  prefix: string,
): ResolvedEnv {
  return profile.kind === "agent"
    ? agentEnv(profile, name, prefix)
    : apiEnv(profile, name, env, prefix);
}

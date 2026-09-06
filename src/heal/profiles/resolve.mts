/**
 * Healer resolution — precedence flag > the recipe's own healer variable >
 * profiles.default > the recipe's default. A flag/env value that names a saved profile
 * wins over the raw grammar, so a profile name never needs an `agent:` prefix;
 * anything else is read as the grammar itself and mapped onto the variables the recipe
 * declares. The platform's own named agent presets (agent-presets.mts) resolve through
 * the recipe's generic custom-command route, so a profile or a flag can name an agent
 * the recipe does not itself ship.
 *
 * What comes back is CONFIGURATION plus one line describing the choice — never a
 * healer, never a process. Mirrors the Fleet's own selection grammar
 * (`fleet.mts:describeSelection`): one line, printed before anything runs, naming what
 * was chosen and why.
 */
import {
  DEFAULT_CONFIG_ENV_PREFIX,
  configKey,
  readConfigValue,
} from "../../config/keys.mts";
import { AGENT_PRESETS, presetCommandTemplate } from "./agent-presets.mts";
import { envFromProfile, type ResolvedEnv } from "./env-from-profile.mts";
import { PROFILES_FILE, type ProfilesFile } from "./profiles.mts";

export const DEFAULT_HEALER = "openai-compatible";

export interface HealerSelection {
  name: string;
  kind: "agent" | "api";
  how: "flag" | "env" | "profile-default" | "default";
  reason: string;
  config: ResolvedEnv;
}

function kindFor(healerName: string): "agent" | "api" {
  return healerName.startsWith("agent:") ? "agent" : "api";
}

/** The raw grammar the recipe reads, mapped onto its own variables. A named agent goes
 *  through the generic custom-command route with the platform's argv for it. */
function envFromGrammar(value: string, prefix: string): ResolvedEnv {
  const healerKey = configKey("HEALER", prefix);
  if (value === DEFAULT_HEALER) {
    return { values: { [healerKey]: value }, secretKeys: [] };
  }
  if (value.startsWith("agent:")) {
    const agentName = value.slice("agent:".length);
    if (agentName === "custom") {
      // The command itself already lives in the recipe's own variable; the
      // configurator has nothing to add and must not invent one.
      return { values: { [healerKey]: value }, secretKeys: [] };
    }
    const preset = AGENT_PRESETS[agentName];
    if (preset) {
      return {
        values: {
          [healerKey]: "agent:custom",
          [configKey("HEALER_AGENT_CMD", prefix)]:
            presetCommandTemplate(preset),
        },
        secretKeys: [],
      };
    }
    throw new Error(
      `unsupported healer "${value}" (expected agent:${Object.keys(AGENT_PRESETS).join(" | agent:")} | agent:custom)`,
    );
  }
  throw new Error(
    `unsupported healer "${value}" (expected ${DEFAULT_HEALER} or agent:<name>)`,
  );
}

function selectionFromProfile(
  profileName: string,
  env: NodeJS.ProcessEnv,
  profiles: ProfilesFile,
  how: HealerSelection["how"],
  reason: string,
  prefix: string,
): HealerSelection {
  const profile = profiles.profiles[profileName];
  return {
    name: profileName,
    kind: profile.kind,
    how,
    reason,
    config: envFromProfile(profile, profileName, env, prefix),
  };
}

/** `value` is either a saved profile name or the recipe's own healer grammar. */
function fromProfileOrGrammar(
  value: string,
  how: "flag" | "env",
  env: NodeJS.ProcessEnv,
  profiles: ProfilesFile | null | undefined,
  reason: string,
  prefix: string,
): HealerSelection {
  if (profiles?.profiles[value]) {
    return selectionFromProfile(value, env, profiles, how, reason, prefix);
  }
  try {
    return {
      name: value,
      kind: kindFor(value),
      how,
      reason,
      config: envFromGrammar(value, prefix),
    };
  } catch (error) {
    if (!profiles) throw error;
    const known = Object.keys(profiles.profiles).join(", ") || "none saved";
    throw new Error(
      `${(error as Error).message} (no saved profile named "${value}" either — known profiles: ${known})`,
    );
  }
}

export function resolveHealer(input: {
  flagValue?: string;
  env?: NodeJS.ProcessEnv;
  profiles?: ProfilesFile | null;
  prefix?: string;
}): HealerSelection {
  const env = input.env ?? process.env;
  const profiles = input.profiles;
  const prefix = input.prefix ?? DEFAULT_CONFIG_ENV_PREFIX;
  const healerKey = configKey("HEALER", prefix);

  if (input.flagValue) {
    return fromProfileOrGrammar(
      input.flagValue,
      "flag",
      env,
      profiles,
      `--healer ${input.flagValue}`,
      prefix,
    );
  }
  const fromEnv = readConfigValue("HEALER", env, prefix);
  if (fromEnv) {
    return fromProfileOrGrammar(
      fromEnv,
      "env",
      env,
      profiles,
      `${healerKey}=${fromEnv}`,
      prefix,
    );
  }
  if (profiles?.default) {
    return selectionFromProfile(
      profiles.default,
      env,
      profiles,
      "profile-default",
      `default: ${PROFILES_FILE} names "${profiles.default}"`,
      prefix,
    );
  }
  return {
    name: DEFAULT_HEALER,
    kind: "api",
    how: "default",
    // The recipe's own default is left to the recipe: setting the variable here would
    // pin a choice the recipe is entitled to change between releases.
    reason: profiles
      ? `default: no ${healerKey}, no default in ${PROFILES_FILE} — the recipe's own default applies`
      : `default: no profiles file, no ${healerKey} — the recipe's own default applies`,
    config: { values: {}, secretKeys: [] },
  };
}

export function describeHealerSelection(selection: HealerSelection): string {
  return `healer: ${selection.name} (${selection.kind}) — ${selection.reason}`;
}

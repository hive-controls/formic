/**
 * `--healer` resolution — precedence flag > FORMIC_HEALER > profiles.default > the
 * legacy env grammar's own default (openai-compatible). A flag/env value that names a
 * saved profile wins over the legacy grammar, so a profile name never needs the
 * `agent:` / `openai-compatible` prefix; anything else is handed to `healerFromEnv`
 * untouched, so its existing typo/refusal messages hold verbatim (Wave 1 verified
 * fact: `heal/cli.test.mts:194-201` pins those messages).
 *
 * Mirrors the Fleet's and the Host's own selection grammar
 * (`fleet.mts:describeSelection`, `host.mts:describeHostSelection`): one line, printed
 * before anything runs, naming what was chosen and why.
 */
import { healerFromEnv } from "../healers/from-env.mts";
import type { Healer } from "../types.mts";
import { healerFromProfile } from "./healer-from-profile.mts";
import type { ProfilesFile } from "./profiles.mts";

export interface HealerSelection {
  healer: Healer;
  name: string;
  kind: "agent" | "api";
  how: "flag" | "env" | "profile-default" | "default";
  reason: string;
}

function kindFor(healerName: string): "agent" | "api" {
  return healerName.startsWith("agent:") ? "agent" : "api";
}

function selectionFromProfile(
  profileName: string,
  env: NodeJS.ProcessEnv,
  profiles: ProfilesFile,
  how: "flag" | "env" | "profile-default",
  reason: string,
): HealerSelection {
  const profile = profiles.profiles[profileName];
  return {
    healer: healerFromProfile(profile, profileName, env),
    name: profileName,
    kind: profile.kind,
    how,
    reason,
  };
}

/** `value` is either a saved profile name or the legacy `FORMIC_HEALER` grammar. */
function fromProfileOrLegacy(
  value: string,
  how: "flag" | "env",
  env: NodeJS.ProcessEnv,
  profiles: ProfilesFile | null | undefined,
  reason: string,
): HealerSelection {
  if (profiles?.profiles[value]) {
    return selectionFromProfile(value, env, profiles, how, reason);
  }
  try {
    const healer = healerFromEnv({ ...env, FORMIC_HEALER: value });
    return { healer, name: value, kind: kindFor(healer.name), how, reason };
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
}): HealerSelection {
  const env = input.env ?? process.env;
  const profiles = input.profiles;

  if (input.flagValue) {
    return fromProfileOrLegacy(
      input.flagValue,
      "flag",
      env,
      profiles,
      `--healer ${input.flagValue}`,
    );
  }
  if (env.FORMIC_HEALER) {
    return fromProfileOrLegacy(
      env.FORMIC_HEALER,
      "env",
      env,
      profiles,
      `FORMIC_HEALER=${env.FORMIC_HEALER}`,
    );
  }
  if (profiles?.default) {
    return selectionFromProfile(
      profiles.default,
      env,
      profiles,
      "profile-default",
      `default: formic.profiles.yaml names "${profiles.default}"`,
    );
  }
  const healer = healerFromEnv(env);
  return {
    healer,
    name: healer.name,
    kind: kindFor(healer.name),
    how: "default",
    reason: profiles
      ? "default: no FORMIC_HEALER, no default in formic.profiles.yaml"
      : "default: no profiles file, no FORMIC_HEALER",
  };
}

/** The model segment of an agent-kind label: the resolved model when one was passed,
 *  "agent default" when none was requested, or an explicit "not passed" refusal when
 *  one was requested but the adapter exposes no route — never a name the harness did
 *  not actually pass. Describes the resolved SELECTION, printed before anything runs;
 *  the audit row, not this label, records what a run actually passed. */
function modelSegment(healer: HealerSelection["healer"]): string {
  if (!healer.modelRequested) return "model: agent default";
  if (healer.modelPassed) return healer.modelVersion;
  return `model: ${healer.modelRequested} (not passed — this agent exposes no model selector)`;
}

export function describeHealerSelection(selection: HealerSelection): string {
  if (selection.kind === "agent") {
    return `healer: ${selection.name} (${selection.healer.name} · ${modelSegment(selection.healer)}) — ${selection.reason}`;
  }
  return `healer: ${selection.name} (${selection.kind}) — ${selection.reason}`;
}

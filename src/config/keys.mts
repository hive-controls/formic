/**
 * The environment keys the configurator resolves INTO.
 *
 * Formic never runs a browser, never spawns a healer and never imports a recipe. What
 * it produces is configuration: a small map of environment variables a recipe already
 * declares in its own toolspec manifest. The suffixes below are the vocabulary the
 * configurator can speak; the manifest's `env[]` decides which of them a given recipe
 * actually receives (run/env.mts drops every undeclared key).
 *
 * The default prefix is the one the first recipe on this contract uses. It is a
 * parameter rather than a constant so a second recipe with a prefix of its own needs
 * no change here — the configurator is recipe-agnostic by construction, not by
 * politeness.
 */

export const DEFAULT_CONFIG_ENV_PREFIX = "E2E_DOCTOR_";

/** Suffixes the configurator knows how to resolve a choice into. */
export const CONFIG_SUFFIXES = [
  "GATE",
  "HOST",
  "HEADED",
  "CDP_URL",
  "CDP_HEADERS",
  "HEALER",
  "HEALER_MODEL",
  "HEALER_BASE_URL",
  "HEALER_API_KEY",
  "HEALER_AGENT_CMD",
  "HEALER_TIMEOUT_MS",
] as const;

export type ConfigSuffix = (typeof CONFIG_SUFFIXES)[number];

/** `configKey("GATE")` is `E2E_DOCTOR_GATE`. */
export function configKey(
  suffix: ConfigSuffix | string,
  prefix: string = DEFAULT_CONFIG_ENV_PREFIX,
): string {
  return `${prefix}${suffix}`;
}

/**
 * Read a key the recipe owns out of an ambient environment, canonical name first and
 * the one-release legacy `FORMIC_` twin second. The configurator only READS these —
 * the deprecation notice belongs to the recipe that owns the name, so nothing is
 * printed here.
 */
export function readConfigValue(
  suffix: ConfigSuffix | string,
  env: NodeJS.ProcessEnv,
  prefix: string = DEFAULT_CONFIG_ENV_PREFIX,
): string | undefined {
  const value = env[configKey(suffix, prefix)] ?? env[`FORMIC_${suffix}`];
  if (value === undefined) return undefined;
  return value.trim() === "" ? undefined : value;
}

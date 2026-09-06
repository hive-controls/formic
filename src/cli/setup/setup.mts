/**
 * Launchie — `e2e-doctor setup`. Detects agents on PATH, asks (or reads flags for
 * `--non-interactive`), runs a smoke heal against the chosen healer, and only THEN
 * writes the Cocoon: the gitignored `.env` (the key) and a committed
 * `formic.profiles.yaml` (everything else). A smoke failure writes nothing at all —
 * not even a partial `.env` line — a fresh key is held only in memory (`deps.env`,
 * mirroring `process.env` in real use) until the smoke passes.
 *
 * Every printed line here is exportable surface (the repo's own rule): the copy
 * about agent/API/CI tradeoffs is printed verbatim, never paraphrased per run.
 */
import { writeFileSync } from "node:fs";
import { join } from "node:path";
import type { ExitCode } from "../../replay/cli.mts";
import {
  ADAPTERS,
  adapterFromCommand,
  type AgentCliAdapter,
} from "../../heal/healers/agent-cli.mts";
import {
  PRESETS,
  PROFILES_FILE,
  loadProfiles,
  saveProfiles,
  validateProfiles,
  type AgentProfile,
  type ApiProfile,
  type HealerProfile,
  type PresetName,
  type ProfilesFile,
} from "../../heal/profiles/profiles.mts";
import { healerFromProfile } from "../../heal/profiles/healer-from-profile.mts";
import { selectGate } from "../../fleet/fleet.mts";
import { actionsSnippet, type SnippetGate } from "./actions-snippet.mts";
import {
  detectAgents,
  describeAgent,
  LOGIN_NOT_CHECKED_NOTE,
  type DetectedAgent,
  type DetectSeams,
} from "./detect.mts";
import { ask, askSecret, choose, type PromptIo } from "./prompts.mts";
import { smokeHeal, type SmokeHealFn, type SmokeResult } from "./smoke.mts";
import { envIsIgnored, gitToplevel, upsertEnvVar } from "./credentials.mts";

export interface SetupDeps {
  cwd: string;
  env: NodeJS.ProcessEnv;
  io: PromptIo;
  log: (line: string) => void;
  error: (line: string) => void;
  detect: DetectSeams;
  fetch?: typeof fetch;
  smoke?: SmokeHealFn;
}

interface PendingWrite {
  varName: string;
  value: string;
}

interface ProfileResolution {
  profile: HealerProfile;
  pendingWrite?: PendingWrite;
}

type Step<T> = { ok: true; value: T } | { ok: false; code: ExitCode };
function fail<T>(code: ExitCode): Step<T> {
  return { ok: false, code };
}
function succeed<T>(value: T): Step<T> {
  return { ok: true, value };
}

type HealerKindChoice =
  { kind: "agent"; agentName: string } | { kind: "api"; preset: PresetName };

const DEFAULT_AGENT_TIMEOUT_MS = 180_000;

const TRADEOFF_COPY =
  "Agents: 30–90 s per repair (measured). APIs: seconds (unverified until the smoke heal runs). CI: Claude Code and Codex are reported to run headless with a vendor token (variable name not verified here); Kimi and Grok: unchecked.";

function flag(argv: string[], name: string): boolean {
  return argv.includes(name);
}
function option(argv: string[], name: string): string | undefined {
  const i = argv.indexOf(name);
  return i >= 0 ? argv[i + 1] : undefined;
}
function timeoutFromFlag(argv: string[]): number | undefined {
  const value = option(argv, "--timeout-ms");
  return value ? Number(value) : undefined;
}

function printBanner(deps: SetupDeps): void {
  deps.log(
    "Launchie writes your Cocoon — a gitignored .env (the key) and a committed formic.profiles.yaml (everything else) — only after a smoke heal passes. Nothing is written on failure.",
  );
  const toplevel = gitToplevel(deps.cwd);
  if (toplevel && toplevel !== deps.cwd) {
    deps.log(
      "warning: e2e-doctor loads .env and formic.profiles.yaml from the directory it runs in",
    );
  }
}

async function printDetected(deps: SetupDeps): Promise<DetectedAgent[]> {
  const detected = await detectAgents(deps.detect);
  for (const agent of detected) deps.log(`  ${describeAgent(agent)}`);
  deps.log(LOGIN_NOT_CHECKED_NOTE);
  return detected;
}

async function chooseHealerKind(
  io: PromptIo,
  detected: DetectedAgent[],
): Promise<HealerKindChoice> {
  const agentOptions = detected
    .filter((agent) => agent.onPath)
    .map((agent) => ({
      label: `agent: ${agent.name}${agent.verified ? " (verified)" : ""}`,
      value: { kind: "agent", agentName: agent.name } as HealerKindChoice,
    }));
  const apiOptions = (Object.keys(PRESETS) as PresetName[]).map((name) => ({
    label: `api: ${name}`,
    value: { kind: "api", preset: name } as HealerKindChoice,
  }));
  return choose(io, "Choose a healer:", [...agentOptions, ...apiOptions]);
}

function agentAdapterFromFlags(
  agentName: string | undefined,
  agentCmd: string | undefined,
): { adapter: AgentCliAdapter; name: string; command?: string } | null {
  if (agentCmd) {
    return {
      adapter: adapterFromCommand(agentCmd),
      name: "custom",
      command: agentCmd,
    };
  }
  if (agentName && ADAPTERS[agentName]) {
    return { adapter: ADAPTERS[agentName], name: agentName };
  }
  return null;
}

function buildAgentProfile(
  argv: string[],
  agentName: string | undefined,
  agentCmd: string | undefined,
  deps: SetupDeps,
): Step<ProfileResolution> {
  const resolved = agentAdapterFromFlags(agentName, agentCmd);
  if (!resolved) {
    deps.error(
      `--agent must be one of ${Object.keys(ADAPTERS).join(", ")}, or pass --agent-cmd`,
    );
    return fail(2);
  }
  const model = option(argv, "--model");
  // Never write a model the adapter would silently drop — the printed healer label
  // and the audit row can only be true to what actually ran.
  if (model && !resolved.adapter.modelArgs && !resolved.adapter.modelEnv) {
    deps.error(
      `agent "${resolved.name}" exposes no model selector — cannot set --model ${model}`,
    );
    return fail(2);
  }
  // The mirror image: a custom command whose template names {model} REQUIRES one —
  // an unfilled token would leave a dangling flag on every spawn.
  if (!model && resolved.adapter.args.includes("{model}")) {
    deps.error(
      `agent "${resolved.name}": command names {model} but no --model was given`,
    );
    return fail(2);
  }
  const profile: AgentProfile = {
    kind: "agent",
    agent: resolved.name,
    timeoutMs: timeoutFromFlag(argv) ?? DEFAULT_AGENT_TIMEOUT_MS,
    ...(resolved.command ? { command: resolved.command } : {}),
    ...(model ? { model } : {}),
  };
  return succeed({ profile });
}

async function fetchModelIds(
  fetchImpl: typeof fetch,
  baseUrl: string,
  apiKey: string | undefined,
): Promise<string[] | null> {
  const headers: Record<string, string> = {};
  if (apiKey) headers.authorization = `Bearer ${apiKey}`;
  const response = await fetchImpl(`${baseUrl.replace(/\/+$/, "")}/models`, {
    headers,
    signal: AbortSignal.timeout(5_000),
  });
  if (!response.ok) return null;
  const body = (await response.json()) as { data?: { id?: string }[] };
  const ids = (body.data ?? [])
    .map((entry) => entry.id)
    .filter((id): id is string => Boolean(id));
  return ids.length > 0 ? ids : null;
}

async function resolveModelInteractively(
  deps: SetupDeps,
  baseUrl: string,
  apiKey: string | undefined,
  defaultModel: string | null,
): Promise<string> {
  const fetchImpl = deps.fetch ?? fetch;
  const listed = await fetchModelIds(fetchImpl, baseUrl, apiKey).catch(
    () => null,
  );
  if (listed) {
    return choose(
      deps.io,
      "Model:",
      listed.map((id) => ({ label: id, value: id })),
    );
  }
  return ask(deps.io, "Model: ", defaultModel ? { default: defaultModel } : {});
}

/** The gate the CI snippet targets: exactly the one the Fleet would pick from this
 *  environment (explicit FORMIC_GATE, else Solari when its key resolves, else local).
 *  A typo or an unready gate falls back to the cloud shape rather than failing setup. */
export function snippetGateFor(env: NodeJS.ProcessEnv): SnippetGate {
  try {
    return selectGate(env).gate.name === "local" ? "local" : "solari";
  } catch {
    return "solari";
  }
}

/** Local presets (ollama, lmstudio, llamacpp, vllm) carry no key; `--api-key-env` opts a
 *  credential back in for a server that wants one. */
function keylessPreset(argv: string[], preset: PresetName): boolean {
  return (
    PRESETS[preset].envKeyName === null &&
    preset !== "custom" &&
    !option(argv, "--api-key-env")
  );
}

function resolveCredentialNonInteractive(
  argv: string[],
  preset: PresetName,
  deps: SetupDeps,
): Step<{ apiKeyFrom?: string }> {
  const store = option(argv, "--api-key-store") ?? "env";
  const varName =
    option(argv, "--api-key-env") ?? PRESETS[preset].envKeyName ?? undefined;
  if (!varName) {
    deps.error("--api-key-env is required for this preset");
    return fail(2);
  }
  if (store === "ci-name" || deps.env[varName]) {
    return succeed({ apiKeyFrom: `env.${varName}` });
  }
  deps.error(
    `${varName} is not set — export it before running --non-interactive, or run setup interactively`,
  );
  return fail(2);
}

async function resolveCredentialInteractive(
  argv: string[],
  preset: PresetName,
  deps: SetupDeps,
): Promise<Step<{ apiKeyFrom?: string; pendingWrite?: PendingWrite }>> {
  const store = await choose(deps.io, "Where should the key live?", [
    { label: "in the gitignored .env on this machine", value: "env" as const },
    {
      label: "CI secret — record the name only, never a value",
      value: "ci-name" as const,
    },
  ]);
  const varName = await ask(deps.io, "Env var name for the key: ", {
    default: PRESETS[preset].envKeyName ?? "",
  });
  if (!varName) {
    deps.error("an env var name is required");
    return fail(2);
  }
  if (store === "ci-name") return succeed({ apiKeyFrom: `env.${varName}` });
  if (deps.env[varName]) {
    const keep = await choose(deps.io, `${varName} is already set — keep it?`, [
      { label: "keep it", value: true },
      { label: "replace it", value: false },
    ]);
    if (keep) return succeed({ apiKeyFrom: `env.${varName}` });
  }
  if (!envIsIgnored(deps.cwd) && !flag(argv, "--yes")) {
    deps.error(
      `.env is not covered by a .gitignore under ${deps.cwd} (up to the git toplevel) — refusing to write a secret there. Add ".env" to .gitignore, or pass --yes.`,
    );
    return fail(2);
  }
  const value = await askSecret(deps.io, "Paste the API key (input hidden): ");
  deps.env[varName] = value;
  return succeed({
    apiKeyFrom: `env.${varName}`,
    pendingWrite: { varName, value },
  });
}

async function buildApiProfileInteractive(
  argv: string[],
  deps: SetupDeps,
  preset: PresetName,
): Promise<Step<ProfileResolution>> {
  const presetInfo = PRESETS[preset];
  const baseUrl =
    option(argv, "--base-url") ??
    presetInfo.baseUrl ??
    (await ask(deps.io, "Base URL: "));
  // A keyless preset (a local server) needs no credential step at all, unless the
  // caller names one explicitly.
  const credential = keylessPreset(argv, preset)
    ? succeed<{ apiKeyFrom?: string; pendingWrite?: PendingWrite }>({})
    : await resolveCredentialInteractive(argv, preset, deps);
  if (!credential.ok) return credential;
  const model =
    option(argv, "--model") ??
    (await resolveModelInteractively(
      deps,
      baseUrl,
      credential.value.pendingWrite?.value,
      presetInfo.defaultModel,
    ));
  const profile: ApiProfile = {
    kind: "api",
    preset,
    baseUrl,
    model,
    apiKeyFrom: credential.value.apiKeyFrom,
    ...(timeoutFromFlag(argv) ? { timeoutMs: timeoutFromFlag(argv) } : {}),
  };
  return succeed({ profile, pendingWrite: credential.value.pendingWrite });
}

function buildApiProfileNonInteractive(
  argv: string[],
  deps: SetupDeps,
): Step<ProfileResolution> {
  const preset = option(argv, "--preset") as PresetName | undefined;
  if (!preset || !(preset in PRESETS)) {
    deps.error(`--preset must be one of ${Object.keys(PRESETS).join(", ")}`);
    return fail(2);
  }
  const baseUrl =
    option(argv, "--base-url") ?? PRESETS[preset].baseUrl ?? undefined;
  const model = option(argv, "--model");
  if (!baseUrl) {
    deps.error(`preset "${preset}" has no default base URL — pass --base-url`);
    return fail(2);
  }
  if (!model) {
    deps.error("--model is required");
    return fail(2);
  }
  const credential = keylessPreset(argv, preset)
    ? succeed<{ apiKeyFrom?: string }>({})
    : resolveCredentialNonInteractive(argv, preset, deps);
  if (!credential.ok) return credential;
  const profile: ApiProfile = {
    kind: "api",
    preset,
    baseUrl,
    model,
    apiKeyFrom: credential.value.apiKeyFrom,
    ...(timeoutFromFlag(argv) ? { timeoutMs: timeoutFromFlag(argv) } : {}),
  };
  return succeed({ profile });
}

function resolveProfileNonInteractive(
  argv: string[],
  deps: SetupDeps,
): Step<ProfileResolution> {
  const kind = option(argv, "--healer-kind");
  if (kind === "agent") {
    return buildAgentProfile(
      argv,
      option(argv, "--agent"),
      option(argv, "--agent-cmd"),
      deps,
    );
  }
  if (kind !== "api") {
    deps.error('--non-interactive requires --healer-kind "agent" or "api"');
    return fail(2);
  }
  return buildApiProfileNonInteractive(argv, deps);
}

async function resolveProfileInteractive(
  argv: string[],
  deps: SetupDeps,
  detected: DetectedAgent[],
): Promise<Step<ProfileResolution>> {
  deps.log(TRADEOFF_COPY);
  const choice = await chooseHealerKind(deps.io, detected);
  if (choice.kind === "agent") {
    return buildAgentProfile(argv, choice.agentName, undefined, deps);
  }
  return buildApiProfileInteractive(argv, deps, choice.preset);
}

function defaultProfileName(profile: HealerProfile): string {
  return profile.kind === "agent" ? `agent-${profile.agent}` : profile.preset;
}

function writeProfile(
  argv: string[],
  deps: SetupDeps,
  profileName: string,
  profile: HealerProfile,
): void {
  const existing = loadProfiles(deps.cwd) ?? { profiles: {} };
  const merged: ProfilesFile = {
    ...existing,
    profiles: { ...existing.profiles, [profileName]: profile },
  };
  if (!existing.default || flag(argv, "--make-default"))
    merged.default = profileName;
  validateProfiles(merged);
  writeFileSync(join(deps.cwd, PROFILES_FILE), saveProfiles(merged));
}

function formatSmokeSuccess(result: SmokeResult): string {
  const seconds = (result.latencyMs / 1000).toFixed(1);
  const usage = result.usage
    ? `; ${result.usage.inputTokens} in / ${result.usage.outputTokens} out tokens`
    : "";
  return `smoke heal: ok in ${seconds} s (${result.kind}${usage})`;
}

async function runSmokeAndWrite(
  argv: string[],
  deps: SetupDeps,
  profileName: string,
  resolution: ProfileResolution,
): Promise<ExitCode> {
  const { profile, pendingWrite } = resolution;
  if (profile.kind === "agent") {
    deps.log(
      "expect 30–90 s (measured on this machine's agents earlier) — waiting for the smoke heal…",
    );
  }
  const healer = healerFromProfile(profile, profileName, deps.env);
  const smokeFn = deps.smoke ?? smokeHeal;
  const result = await smokeFn(healer, { timeoutMs: profile.timeoutMs }).catch(
    (error: Error) => ({
      ok: false as const,
      latencyMs: 0,
      error: error.message,
    }),
  );
  if (!result.ok) {
    deps.error(`smoke heal failed: ${result.error ?? "unknown error"}`);
    return 1;
  }
  deps.log(formatSmokeSuccess(result));
  if (result.kind && result.kind !== "no-repair") {
    deps.log(
      `warning: the healer proposed "${result.kind}" on a synthetic context with nothing to repair — expected no-repair`,
    );
  }
  if (pendingWrite)
    upsertEnvVar(deps.cwd, pendingWrite.varName, pendingWrite.value);
  writeProfile(argv, deps, profileName, profile);
  if (pendingWrite)
    deps.log(`${pendingWrite.varName} written to ${join(deps.cwd, ".env")}`);
  deps.log(
    `profile "${profileName}" written to ${join(deps.cwd, PROFILES_FILE)}`,
  );
  deps.log(actionsSnippet(profileName, profile, snippetGateFor(deps.env)));
  return 0;
}

export async function runSetup(
  argv: string[],
  deps: SetupDeps,
): Promise<ExitCode> {
  const nonInteractive = flag(argv, "--non-interactive");
  if (!deps.io.isTty && !nonInteractive) {
    deps.error(
      "not a TTY — pass --non-interactive with --healer-kind <api|agent>, --agent <name> | --preset <name> [--base-url <url>] [--model <name>] [--api-key-env <VAR>, not needed for a keyless local preset] [--timeout-ms <ms>], --profile-name <name>",
    );
    return 2;
  }
  printBanner(deps);
  const detected = await printDetected(deps);

  const resolution = nonInteractive
    ? resolveProfileNonInteractive(argv, deps)
    : await resolveProfileInteractive(argv, deps, detected);
  if (!resolution.ok) return resolution.code;

  const profileName = nonInteractive
    ? option(argv, "--profile-name")
    : (option(argv, "--profile-name") ??
      (await ask(deps.io, "Profile name: ", {
        default: defaultProfileName(resolution.value.profile),
      })));
  if (!profileName) {
    deps.error("--profile-name is required");
    return 2;
  }
  return runSmokeAndWrite(argv, deps, profileName, resolution.value);
}

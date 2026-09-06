/**
 * `formic run <recipe> [args…]` — resolve configuration, then get out of the way.
 *
 * The launcher reads the recipe's toolspec manifest, resolves the gate and healer
 * choices into the environment keys that manifest declares, prints one line per choice
 * (secrets redacted), and hands control to the recipe's own command with stdio
 * inherited and its exit code forwarded. It never interprets the recipe's arguments and
 * never touches its output.
 *
 * ARGUMENT SPLIT. A manifest's `launch.args` is one program prefix plus one default
 * operation (`--import tsx <script> heal {spec}`). Caller arguments replace the
 * operation, never the prefix, so `formic run e2e-doctor replay spec.yaml` is the same
 * process the recipe's own CLI would have started. The boundary is found the only way a
 * generic launcher honestly can: the prefix ends at the LAST argument that names a file
 * that exists — the script the runtime is being asked to run. A manifest whose command
 * is the recipe's own binary has no such argument and therefore no prefix, which is the
 * right answer for that shape too.
 */
import { spawn } from "node:child_process";
import { existsSync, statSync } from "node:fs";
import { isAbsolute, join } from "node:path";
import type { ToolManifest } from "./types.mts";
import { describeSelection, selectGate } from "../fleet/fleet.mts";
import {
  describeHealerSelection,
  resolveHealer,
} from "../heal/profiles/resolve.mts";
import { loadProfiles, type ProfilesFile } from "../heal/profiles/profiles.mts";
import { DEFAULT_CONFIG_ENV_PREFIX } from "../config/keys.mts";
import { buildRunEnv, loadDotenv } from "./env.mts";
import { resolveManifest } from "./manifest.mts";

export interface RunOptions {
  recipe: string;
  args: string[];
  cwd: string;
  env: NodeJS.ProcessEnv;
  manifestPath?: string;
  /** Name of a saved profile; overrides the profiles file's own default. */
  profile?: string;
  /** Profiles to use INSTEAD of the file on disk — the wizard's candidate profile,
   *  which is deliberately not written until a smoke run passes with it. */
  profiles?: ProfilesFile | null;
  configPrefix?: string;
  log: (line: string) => void;
  /** Test seam; defaults to a real child process with inherited stdio. */
  spawnChild?: (
    command: string,
    argv: string[],
    options: { cwd: string; env: NodeJS.ProcessEnv },
  ) => Promise<number>;
}

function existsAsFile(candidate: string, cwd: string): boolean {
  const path = isAbsolute(candidate) ? candidate : join(cwd, candidate);
  try {
    return existsSync(path) && statSync(path).isFile();
  } catch {
    return false;
  }
}

/** Split `launch.args` into the program prefix and the default operation. */
export function splitLaunchArgs(
  args: string[],
  cwd: string,
): { prefix: string[]; operation: string[] } {
  let lastFileIndex = -1;
  for (let index = 0; index < args.length; index++) {
    if (existsAsFile(args[index], cwd)) lastFileIndex = index;
  }
  return {
    prefix: args.slice(0, lastFileIndex + 1),
    operation: args.slice(lastFileIndex + 1),
  };
}

function launchCwd(manifest: ToolManifest, cwd: string): string {
  const declared = manifest.launch.cwd;
  if (!declared || declared === "{repo}") return cwd;
  return isAbsolute(declared) ? declared : join(cwd, declared);
}

function profilesFor(
  cwd: string,
  profileName: string | undefined,
  override: ProfilesFile | null | undefined,
): ProfilesFile | null {
  const profiles = override !== undefined ? override : loadProfiles(cwd);
  if (!profileName) return profiles;
  if (!profiles?.profiles[profileName]) {
    const known = Object.keys(profiles?.profiles ?? {}).join(", ") || "none";
    throw new Error(`--profile ${profileName} is not saved (known: ${known})`);
  }
  return { ...profiles, default: profileName };
}

function spawnInherited(
  command: string,
  argv: string[],
  options: { cwd: string; env: NodeJS.ProcessEnv },
): Promise<number> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, argv, { ...options, stdio: "inherit" });
    child.on("error", reject);
    child.on("close", (code, signal) => resolve(code ?? (signal ? 130 : 1)));
  });
}

export async function runRecipe(options: RunOptions): Promise<number> {
  const prefix = options.configPrefix ?? DEFAULT_CONFIG_ENV_PREFIX;
  const { manifest, path, schemaChecked } = await resolveManifest(
    options.recipe,
    options.cwd,
    options.manifestPath,
  );
  options.log(`manifest: ${path}`);
  if (!schemaChecked) {
    options.log(
      "  schema not checked — @hive-controls/toolspec is not installed here; only the fields this launcher reads were verified",
    );
  }

  const profiles = profilesFor(options.cwd, options.profile, options.profiles);
  const gate = selectGate(options.env, prefix);
  const healer = resolveHealer({ env: options.env, profiles, prefix });
  options.log(describeSelection(gate));
  options.log(describeHealerSelection(healer));

  const runEnv = buildRunEnv({
    manifest,
    resolved: { ...gate.config, ...healer.config.values },
    resolvedSecretKeys: healer.config.secretKeys,
    ambient: options.env,
    dotenv: loadDotenv(options.cwd),
  });
  for (const line of runEnv.lines) options.log(`  ${line}`);
  for (const name of runEnv.dropped) {
    options.log(
      `  ${name} not set — ${options.recipe}'s manifest does not declare it`,
    );
  }

  const childCwd = launchCwd(manifest, options.cwd);
  const split = splitLaunchArgs(manifest.launch.args ?? [], childCwd);
  const argv =
    options.args.length > 0
      ? [...split.prefix, ...options.args]
      : [...split.prefix, ...split.operation];
  const spawnChild = options.spawnChild ?? spawnInherited;
  return spawnChild(manifest.launch.command, argv, {
    cwd: childCwd,
    env: { ...options.env, ...runEnv.values },
  });
}

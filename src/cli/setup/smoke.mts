/**
 * The smoke check Launchie runs before writing anything (setup.mts): does the
 * configuration it is about to save actually start a run?
 *
 * It answers that the only way a platform with no recipe dependency can — by running
 * the recipe, through `formic run`, exactly as a user would. Three modes, in the order
 * they become possible, each named in the result so nothing claims more than it did:
 *
 *   - `replay` — the recipe ships the fixture convention (`fixtures/specs/*.yaml` beside
 *     `fixtures/sample-app`), so one real replay runs end to end under the resolved
 *     configuration. The strongest answer available.
 *   - `health` — the recipe is installed but ships no fixture; its manifest's own
 *     `health.check` runs instead. Proves installed-and-runnable, not a replay.
 *   - `config-only` — no manifest found, so the recipe is not installed here. The
 *     configuration is written on the caller's judgement and the line says so.
 *
 * What this can no longer do is call the healer. The platform holds no healer to call:
 * it resolves a healer CHOICE into environment variables and the recipe spawns it. A
 * bad key now surfaces on the first heal rather than in the wizard — the honest cost of
 * a platform that depends on no recipe, printed rather than papered over.
 */
import { spawn } from "node:child_process";
import { existsSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import type { ProfilesFile } from "../../heal/profiles/profiles.mts";
import { resolveManifest } from "../../run/manifest.mts";
import { runRecipe } from "../../run/run.mts";

export type SmokeMode = "replay" | "health" | "config-only";

export interface SmokeResult {
  ok: boolean;
  mode: SmokeMode;
  latencyMs: number;
  detail: string;
}

export interface SmokeOptions {
  recipe: string;
  cwd: string;
  env: NodeJS.ProcessEnv;
  profile?: string;
  /** The candidate profile, held in memory: nothing is written until this passes. */
  profiles?: ProfilesFile | null;
  log: (line: string) => void;
}

export type SmokeFn = (options: SmokeOptions) => Promise<SmokeResult>;

/** The fixture convention a recipe opts into by shipping it: one or more specs beside
 *  the app they run against, both under the manifest's own directory. */
export function fixturePair(
  manifestPath: string,
): { spec: string; app: string } | null {
  const root = dirname(manifestPath);
  const specsDirectory = join(root, "fixtures", "specs");
  const app = join(root, "fixtures", "sample-app");
  if (!existsSync(specsDirectory) || !existsSync(app)) return null;
  const spec = readdirSync(specsDirectory)
    .filter((entry) => entry.endsWith(".yaml"))
    .sort()[0];
  return spec ? { spec: join(specsDirectory, spec), app } : null;
}

function runHealthCheck(argv: string[], cwd: string): Promise<number> {
  const [command, ...rest] = argv;
  return new Promise((resolve) => {
    const child = spawn(command, rest, { cwd, stdio: "ignore" });
    child.on("error", () => resolve(1));
    child.on("close", (code) => resolve(code ?? 1));
  });
}

export const smokeRun: SmokeFn = async (options) => {
  const startedAt = Date.now();
  const elapsed = () => Date.now() - startedAt;
  let manifestPath: string;
  let healthArgv: string[] | undefined;
  try {
    const loaded = await resolveManifest(options.recipe, options.cwd);
    manifestPath = loaded.path;
    healthArgv = loaded.manifest.health?.check;
  } catch (error) {
    return {
      ok: true,
      mode: "config-only",
      latencyMs: elapsed(),
      detail: `${(error as Error).message} — configuration written unchecked`,
    };
  }

  const fixture = fixturePair(manifestPath);
  if (fixture) {
    const code = await runRecipe({
      recipe: options.recipe,
      args: ["replay", fixture.spec, "--app", fixture.app],
      cwd: options.cwd,
      env: options.env,
      profile: options.profile,
      profiles: options.profiles,
      log: options.log,
    }).catch(() => 1);
    return {
      ok: code === 0,
      mode: "replay",
      latencyMs: elapsed(),
      detail: `formic run ${options.recipe} replay ${fixture.spec} exited ${code}`,
    };
  }
  if (!healthArgv || healthArgv.length === 0) {
    return {
      ok: true,
      mode: "config-only",
      latencyMs: elapsed(),
      detail: `${options.recipe} ships neither a fixture nor a health check — configuration written unchecked`,
    };
  }
  const code = await runHealthCheck(healthArgv, options.cwd);
  return {
    ok: code === 0,
    mode: "health",
    latencyMs: elapsed(),
    detail: `${options.recipe}'s own health check exited ${code}`,
  };
};

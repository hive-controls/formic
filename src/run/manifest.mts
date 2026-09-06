/**
 * Finding and reading a recipe's toolspec manifest.
 *
 * The search order is the one a person would try by hand, cheapest first:
 *
 *   1. `--manifest <path>` — an explicit answer always wins.
 *   2. `./<recipe>.toolspec.yaml` — the recipe's own file, in the current directory.
 *   3. `node_modules/<recipe>/<recipe>.toolspec.yaml` — the recipe as a dependency.
 *   4. `.hivedeck/tools/<recipe>.yaml` — the repo-local operator convention.
 *   5. any `<recipe>.toolspec.yaml` under the working tree — the shipped-manifest walk
 *      the operator UI performs, with the same excluded directories, so a manifest a
 *      launcher can see is one this command can run.
 *
 * Steps 4 and 5 mirror `discovery.Discover` in the operator UI
 * (internal/discovery/{catalog,product}.go): the same two conventions, the same
 * `node_modules`/`dist`/`build`/`vendor`/`testdata`/dot-directory exclusions. A manifest
 * this command cannot find is one the UI would not have offered either.
 */
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { parse as parseYaml } from "yaml";
import type { ToolManifest } from "./types.mts";

/** Vendor/build/state/fixture directories the shipped-manifest walk never enters. */
const EXCLUDED_DIRECTORIES = new Set([
  "node_modules",
  "dist",
  "build",
  "vendor",
  "testdata",
]);

const MANIFEST_SUFFIX = ".toolspec.yaml";

export interface LoadedManifest {
  manifest: ToolManifest;
  path: string;
  /** False when the contract package was not installed and only the launcher's own
   *  structural check ran. */
  schemaChecked: boolean;
}

function walkForManifest(root: string, fileName: string): string | null {
  let entries: string[];
  try {
    entries = readdirSync(root);
  } catch {
    return null;
  }
  const directories: string[] = [];
  for (const entry of entries) {
    const candidate = join(root, entry);
    let isDirectory = false;
    try {
      isDirectory = statSync(candidate).isDirectory();
    } catch {
      continue;
    }
    if (isDirectory) {
      if (!EXCLUDED_DIRECTORIES.has(entry) && !entry.startsWith(".")) {
        directories.push(candidate);
      }
    } else if (entry === fileName) {
      return candidate;
    }
  }
  for (const directory of directories) {
    const found = walkForManifest(directory, fileName);
    if (found) return found;
  }
  return null;
}

/** Every place a recipe's manifest is looked for, in order. */
export function manifestCandidates(recipe: string, cwd: string): string[] {
  return [
    join(cwd, `${recipe}${MANIFEST_SUFFIX}`),
    join(cwd, "node_modules", recipe, `${recipe}${MANIFEST_SUFFIX}`),
    join(cwd, ".hivedeck", "tools", `${recipe}.yaml`),
  ];
}

export function findManifest(
  recipe: string,
  cwd: string,
  explicitPath?: string,
): string {
  if (explicitPath) {
    if (!existsSync(explicitPath)) {
      throw new Error(`--manifest ${explicitPath} does not exist`);
    }
    return explicitPath;
  }
  for (const candidate of manifestCandidates(recipe, cwd)) {
    if (existsSync(candidate)) return candidate;
  }
  const walked = walkForManifest(cwd, `${recipe}${MANIFEST_SUFFIX}`);
  if (walked) return walked;
  throw new Error(
    `no manifest for "${recipe}" — looked for ${manifestCandidates(recipe, cwd).join(", ")} and any ${recipe}${MANIFEST_SUFFIX} under ${cwd}. Pass --manifest <path>.`,
  );
}

/**
 * Validation comes from the contract package when it is installed, and from the
 * structural check below when it is not.
 *
 * The contract package is the authority — it owns the schema — but it is a separate
 * install, and a launcher that refused to run a recipe because a validator was missing
 * would be failing at the wrong thing. So the schema is used when present, the shape
 * the launcher itself depends on is checked always, and the weaker mode says so out
 * loud rather than passing silently for the wrong reason.
 */
export interface ManifestCheck {
  valid: boolean;
  errors: string[];
  /** True when the contract package's own schema was the one that ran. */
  schemaChecked: boolean;
}

/** The fields this launcher itself reads. Never a substitute for the schema — just the
 *  subset whose absence would make the next few lines throw something unhelpful. */
function structuralCheck(candidate: unknown): string[] {
  const errors: string[] = [];
  if (typeof candidate !== "object" || candidate === null) {
    return ["a manifest must be a mapping"];
  }
  const manifest = candidate as Record<string, unknown>;
  if (manifest.toolspec !== 1) errors.push("toolspec must be 1");
  if (typeof manifest.name !== "string" || manifest.name === "") {
    errors.push("name is required");
  }
  const launch = manifest.launch as Record<string, unknown> | undefined;
  if (!launch || typeof launch.command !== "string" || launch.command === "") {
    errors.push("launch.command is required");
  }
  return errors;
}

export async function checkManifest(
  candidate: unknown,
): Promise<ManifestCheck> {
  const structural = structuralCheck(candidate);
  if (structural.length > 0) {
    return { valid: false, errors: structural, schemaChecked: false };
  }
  try {
    // Assembled rather than written out so the compiler cannot resolve it either:
    // the contract package is genuinely optional at build time as well as at run
    // time, and the launcher's own view of a manifest is types.mts.
    const contractSpecifier: string = ["@hive-controls", "toolspec"].join("/");
    const contract = (await import(contractSpecifier)) as {
      validate(value: unknown): { valid: boolean; errors: string[] };
    };
    const result = contract.validate(candidate);
    return {
      valid: result.valid,
      errors: result.errors,
      schemaChecked: true,
    };
  } catch {
    return { valid: true, errors: [], schemaChecked: false };
  }
}

export async function loadManifest(path: string): Promise<LoadedManifest> {
  const parsed: unknown = parseYaml(readFileSync(path, "utf8"));
  const check = await checkManifest(parsed);
  if (!check.valid) {
    throw new Error(
      `${path} is not a valid toolspec manifest:\n  - ${check.errors.join("\n  - ")}`,
    );
  }
  return {
    manifest: parsed as ToolManifest,
    path,
    schemaChecked: check.schemaChecked,
  };
}

export function resolveManifest(
  recipe: string,
  cwd: string,
  explicitPath?: string,
): Promise<LoadedManifest> {
  return loadManifest(findManifest(recipe, cwd, explicitPath));
}

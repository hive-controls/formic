import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { test } from "node:test";
import assert from "node:assert/strict";
import { parse as parseYaml } from "yaml";
import { validate } from "./index.mts";

const HERE = dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = join(HERE, "..", "..", "..");

// The manifests each Formic product ships beside its own entry point — E2E Doctor and
// the four product-shaped dev probes (packages/harness/probes/) that the launcher's
// built-in adapter surfaces on the same footing as any third-party tool.
const PRODUCT_MANIFEST_PATHS = [
  "../../harness/src/cli/e2e-doctor.toolspec.yaml",
  "../../harness/probes/cleanup-sandboxes.toolspec.yaml",
  "../../harness/probes/gate-browserstack.toolspec.yaml",
  "../../harness/probes/gate-saucelabs.toolspec.yaml",
  "../../harness/probes/host-app-in-solari.toolspec.yaml",
];

interface ManifestShape {
  launch: { args?: string[] };
  health?: { check?: string[] };
}

function loadManifest(relativePath: string): unknown {
  const text = readFileSync(join(HERE, relativePath), "utf8");
  return parseYaml(text);
}

/** The `.mts` file a manifest's own `launch.args` names — repo-root-relative, the same
 *  string a `launch.cwd: "{repo}"` argv would resolve against. Every shipped manifest
 *  names exactly one; a manifest with none is a fixture bug, not a tool without an
 *  entrypoint (the launcher's own npm-scripts adapter is the only case with none, and
 *  it is never a manifest file). */
function entrypointOf(manifest: ManifestShape): string {
  const entry = (manifest.launch.args ?? []).find((arg) =>
    arg.endsWith(".mts"),
  );
  if (entry === undefined) {
    throw new Error("launch.args names no .mts entrypoint");
  }
  return entry;
}

test("every shipped product manifest parses as YAML and validates", () => {
  for (const relativePath of PRODUCT_MANIFEST_PATHS) {
    const manifest = loadManifest(relativePath);
    const result = validate(manifest);
    assert.equal(
      result.valid,
      true,
      `${relativePath} should validate; errors: ${result.errors.join(", ")}`,
    );
  }
});

test("every shipped manifest's launch entrypoint file exists in the repo", () => {
  // Schema/shape validation alone cannot catch a launch.args typo or a moved file —
  // a manifest naming `definitely-not-a-command`/`missing.mts` validates clean against
  // the schema, since the schema only knows launch.command/args are strings.
  for (const relativePath of PRODUCT_MANIFEST_PATHS) {
    const manifest = loadManifest(relativePath) as ManifestShape;
    const entrypoint = entrypointOf(manifest);
    assert.ok(
      existsSync(join(REPO_ROOT, entrypoint)),
      `${relativePath}: launch names ${entrypoint}, which does not exist in the repo`,
    );
  }
});

test("every shipped manifest's health check invokes its own entrypoint, not just the tsx runtime", () => {
  // All five health checks sharing a generic ["npx", "tsx", "--version"] line proved
  // nothing tool-specific: a manifest could point launch at the wrong file entirely and
  // still report healthy, because health.check never referenced launch's own entrypoint.
  for (const relativePath of PRODUCT_MANIFEST_PATHS) {
    const manifest = loadManifest(relativePath) as ManifestShape;
    const entrypoint = entrypointOf(manifest);
    const healthCheck = manifest.health?.check ?? [];
    assert.ok(
      healthCheck.includes(entrypoint),
      `${relativePath}: health.check ${JSON.stringify(healthCheck)} never names the tool's own entrypoint ${entrypoint}`,
    );
  }
});

test("a deliberately broken copy of a shipped manifest is rejected", () => {
  const manifest = loadManifest(PRODUCT_MANIFEST_PATHS[0]) as Record<
    string,
    unknown
  >;
  const broken = { ...manifest };
  delete broken.launch;
  const result = validate(broken);
  assert.equal(
    result.valid,
    false,
    "a manifest without launch must be rejected",
  );
  const matchingError = result.errors.find((error) => error.includes("launch"));
  assert.ok(
    matchingError,
    `expected an error naming the missing launch block; got: ${result.errors.join(", ")}`,
  );
});

/**
 * The two rules that make a resolved environment safe: only keys the manifest declares
 * reach the recipe, and a secret is carried by name with `***` in every printed line.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import type { ToolManifest } from "./types.mts";
import { buildRunEnv, parseDotenv } from "./env.mts";

const MANIFEST = {
  toolspec: 1,
  name: "recipe",
  title: "Recipe",
  description: "A recipe.",
  launch: { command: "node", args: [] },
  env: [
    { name: "RECIPE_GATE", description: "gate" },
    { name: "RECIPE_KEY", description: "key", secret: true },
    { name: "RECIPE_MODEL", description: "model", default: "m-default" },
  ],
} as unknown as ToolManifest;

test("an undeclared resolved key is dropped and named — never dropped in silence", () => {
  const built = buildRunEnv({
    manifest: MANIFEST,
    resolved: { RECIPE_GATE: "cdp", RECIPE_CDP_URL: "wss://x/" },
    ambient: {},
  });
  assert.deepEqual(built.values, { RECIPE_GATE: "cdp" });
  assert.deepEqual(built.dropped, ["RECIPE_CDP_URL"]);
});

test("a secret is passed by value but printed as ***", () => {
  const built = buildRunEnv({
    manifest: MANIFEST,
    resolved: {},
    ambient: { RECIPE_KEY: "sk-live-1234" },
  });
  assert.equal(built.values.RECIPE_KEY, "sk-live-1234");
  assert.deepEqual(built.lines, ["RECIPE_KEY=***"]);
  assert.equal(
    built.lines.some((line) => line.includes("sk-live-1234")),
    false,
  );
});

test("a key the configurator resolved is redacted too when it names it secret", () => {
  const built = buildRunEnv({
    manifest: MANIFEST,
    resolved: { RECIPE_MODEL: "m" },
    resolvedSecretKeys: ["RECIPE_MODEL"],
    ambient: {},
  });
  assert.deepEqual(built.lines, ["RECIPE_MODEL=***"]);
});

test("precedence: a resolved choice beats the ambient environment, which beats .env", () => {
  const built = buildRunEnv({
    manifest: MANIFEST,
    resolved: { RECIPE_GATE: "resolved" },
    ambient: { RECIPE_GATE: "ambient", RECIPE_MODEL: "ambient-model" },
    dotenv: { RECIPE_GATE: "dotenv", RECIPE_MODEL: "dotenv-model" },
  });
  assert.equal(built.values.RECIPE_GATE, "resolved");
  assert.equal(built.values.RECIPE_MODEL, "ambient-model");
});

test("a declared default is left to the recipe, never re-sent as an explicit value", () => {
  const built = buildRunEnv({
    manifest: MANIFEST,
    resolved: {},
    ambient: {},
  });
  assert.equal("RECIPE_MODEL" in built.values, false);
});

test("the launcher's own status file passes through even though no recipe declares it", () => {
  const built = buildRunEnv({
    manifest: MANIFEST,
    resolved: {},
    ambient: { HIVEDECK_STATUS_FILE: "/tmp/status.ndjson" },
  });
  assert.equal(built.values.HIVEDECK_STATUS_FILE, "/tmp/status.ndjson");
  assert.deepEqual(built.lines, []);
});

test("the .env reader takes KEY=value, strips one layer of quotes, ignores comments", () => {
  const values = parseDotenv(
    ["# a comment", "A=1", 'B="two"', "C='three'", "", "D=has=equals"].join(
      "\n",
    ),
  );
  assert.deepEqual(values, { A: "1", B: "two", C: "three", D: "has=equals" });
});

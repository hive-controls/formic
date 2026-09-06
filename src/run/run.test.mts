/**
 * `formic run` against a FIXTURE manifest — no recipe is installed anywhere in this
 * suite. The fixture's launch command is `node` running a script that prints the
 * environment it was given, so the assertions are about what the launcher actually
 * handed a child process, not about what it said it would.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { findManifest } from "./manifest.mts";
import { runRecipe, splitLaunchArgs } from "./run.mts";

const MANIFEST = `toolspec: 1
name: fixture
title: Fixture
description: A fixture recipe whose launch command prints its own environment.
launch:
  command: node
  args: ["print-env.mjs", "heal", "{spec}"]
  cwd: "{repo}"
env:
  - name: E2E_DOCTOR_GATE
    description: which gate
  - name: E2E_DOCTOR_HEALER
    description: which healer
  - name: E2E_DOCTOR_HEALER_AGENT_CMD
    description: the agent command template
  - name: E2E_DOCTOR_HEALER_API_KEY
    description: the healer key
    secret: true
`;

function fixtureRoot(): string {
  const root = mkdtempSync(join(tmpdir(), "formic-run-"));
  writeFileSync(join(root, "fixture.toolspec.yaml"), MANIFEST);
  writeFileSync(
    join(root, "print-env.mjs"),
    [
      "const keys = Object.keys(process.env).filter((k) => k.startsWith('E2E_DOCTOR_'));",
      "console.log(JSON.stringify({ argv: process.argv.slice(2), env: Object.fromEntries(keys.map((k) => [k, process.env[k]])) }));",
    ].join("\n"),
  );
  return root;
}

interface Captured {
  command: string;
  argv: string[];
  env: NodeJS.ProcessEnv;
}

function capturingSpawn(captured: Captured[], code = 0) {
  return async (
    command: string,
    argv: string[],
    options: { cwd: string; env: NodeJS.ProcessEnv },
  ) => {
    captured.push({ command, argv, env: options.env });
    return code;
  };
}

test("the program prefix survives and the caller's arguments replace the default operation", () => {
  const root = fixtureRoot();
  try {
    const split = splitLaunchArgs(["print-env.mjs", "heal", "{spec}"], root);
    assert.deepEqual(split.prefix, ["print-env.mjs"]);
    assert.deepEqual(split.operation, ["heal", "{spec}"]);
    // A manifest whose command is the recipe's own binary has no prefix at all.
    assert.deepEqual(splitLaunchArgs(["heal", "{spec}"], root).prefix, []);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("only declared keys reach the child, and the secret is never printed", async () => {
  const root = fixtureRoot();
  const captured: Captured[] = [];
  const lines: string[] = [];
  try {
    const code = await runRecipe({
      recipe: "fixture",
      args: ["replay", "specs/a.yaml"],
      cwd: root,
      env: {
        SOLARI_API_KEY: "slr_test",
        E2E_DOCTOR_HEALER_API_KEY: "sk-live-9",
        E2E_DOCTOR_EVIDENCE_DIR: "undeclared",
      },
      log: (line) => lines.push(line),
      spawnChild: capturingSpawn(captured),
    });
    assert.equal(code, 0);
    assert.deepEqual(captured[0].argv, [
      "print-env.mjs",
      "replay",
      "specs/a.yaml",
    ]);
    assert.equal(captured[0].env.E2E_DOCTOR_GATE, "solari");
    assert.equal(captured[0].env.E2E_DOCTOR_HEALER_API_KEY, "sk-live-9");
    assert.equal(
      lines.some((line) => line.includes("sk-live-9")),
      false,
    );
    assert.equal(
      lines.some((line) => line === "  E2E_DOCTOR_HEALER_API_KEY=***"),
      true,
    );
    assert.equal(
      lines.some((line) =>
        line.includes("gate: solari (Outside) — default: SOLARI_API_KEY"),
      ),
      true,
    );
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a profile resolves into the recipe's own variables before the child starts", async () => {
  const root = fixtureRoot();
  const captured: Captured[] = [];
  try {
    writeFileSync(
      join(root, "formic.profiles.yaml"),
      [
        "default: kimi",
        "profiles:",
        "  kimi:",
        "    kind: agent",
        "    agent: kimi",
        "",
      ].join("\n"),
    );
    await runRecipe({
      recipe: "fixture",
      args: [],
      cwd: root,
      env: {},
      log: () => {},
      spawnChild: capturingSpawn(captured),
    });
    assert.equal(captured[0].env.E2E_DOCTOR_HEALER, "agent:custom");
    assert.equal(
      captured[0].env.E2E_DOCTOR_HEALER_AGENT_CMD,
      "kimi -p {prompt}",
    );
    // No caller arguments: the manifest's own default operation runs.
    assert.deepEqual(captured[0].argv, ["print-env.mjs", "heal", "{spec}"]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the child's exit code is what `formic run` returns", async () => {
  const root = fixtureRoot();
  const captured: Captured[] = [];
  try {
    const code = await runRecipe({
      recipe: "fixture",
      args: [],
      cwd: root,
      env: {},
      log: () => {},
      spawnChild: capturingSpawn(captured, 3),
    });
    assert.equal(code, 3);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a real child process receives exactly the environment the launcher printed", async () => {
  const root = fixtureRoot();
  const lines: string[] = [];
  const output: string[] = [];
  try {
    const code = await runRecipe({
      recipe: "fixture",
      args: ["replay"],
      cwd: root,
      env: { PATH: process.env.PATH, SOLARI_API_KEY: "slr_test" },
      log: (line) => lines.push(line),
      spawnChild: async (command, argv, options) => {
        const { execFile } = await import("node:child_process");
        return new Promise<number>((resolve) => {
          execFile(
            command,
            argv,
            { cwd: options.cwd, env: options.env },
            (_error, stdout) => {
              output.push(stdout);
              resolve(0);
            },
          );
        });
      },
    });
    assert.equal(code, 0);
    const printed = JSON.parse(output[0]) as {
      argv: string[];
      env: Record<string, string>;
    };
    assert.deepEqual(printed.argv, ["replay"]);
    assert.deepEqual(printed.env, { E2E_DOCTOR_GATE: "solari" });
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the manifest search finds the repo-local operator convention and the shipped walk", () => {
  const root = mkdtempSync(join(tmpdir(), "formic-find-"));
  try {
    mkdirSync(join(root, ".hivedeck", "tools"), { recursive: true });
    writeFileSync(join(root, ".hivedeck", "tools", "local.yaml"), MANIFEST);
    assert.equal(
      findManifest("local", root),
      join(root, ".hivedeck", "tools", "local.yaml"),
    );

    mkdirSync(join(root, "recipes", "deep"), { recursive: true });
    writeFileSync(
      join(root, "recipes", "deep", "walked.toolspec.yaml"),
      MANIFEST,
    );
    assert.equal(
      findManifest("walked", root),
      join(root, "recipes", "deep", "walked.toolspec.yaml"),
    );

    // The walk skips the same directories the operator UI's walk skips.
    mkdirSync(join(root, "node_modules", "hidden"), { recursive: true });
    writeFileSync(
      join(root, "node_modules", "hidden", "buried.toolspec.yaml"),
      MANIFEST,
    );
    assert.throws(() => findManifest("buried", root), /no manifest for/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

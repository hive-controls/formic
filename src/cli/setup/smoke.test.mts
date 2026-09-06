/**
 * The wizard's smoke check, offline: which mode it picks, and that a missing recipe
 * degrades to a config-only pass with the reason in the line rather than a refusal.
 * No browser, no agent spawn and no network egress anywhere here — the `health` mode
 * runs `node --version`, and the `replay` mode is exercised through a fixture recipe
 * whose launch command is `node -e`.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fixturePair, smokeRun } from "./smoke.mts";

function scratchDirectory(): string {
  return mkdtempSync(join(tmpdir(), "formic-smoke-"));
}

function writeManifest(root: string, name: string, body: string): void {
  writeFileSync(join(root, `${name}.toolspec.yaml`), body);
}

const HEALTH_ONLY = `toolspec: 1
name: healthy
title: Healthy
description: A recipe that ships a health check and no fixture.
launch:
  command: node
  args: ["-e", "process.exit(0)"]
health:
  check: ["node", "--version"]
`;

test("no manifest at all is a config-only pass that names what it could not find", async () => {
  const root = scratchDirectory();
  try {
    const result = await smokeRun({
      recipe: "absent",
      cwd: root,
      env: {},
      log: () => {},
    });
    assert.equal(result.ok, true);
    assert.equal(result.mode, "config-only");
    assert.match(result.detail, /no manifest for "absent"/);
    assert.match(result.detail, /written unchecked/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a recipe with a health check and no fixture runs the health check", async () => {
  const root = scratchDirectory();
  try {
    writeManifest(root, "healthy", HEALTH_ONLY);
    const result = await smokeRun({
      recipe: "healthy",
      cwd: root,
      env: {},
      log: () => {},
    });
    assert.equal(result.mode, "health");
    assert.equal(result.ok, true);
    assert.match(result.detail, /health check exited 0/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("a failing health check is not ok, and the exit code is reported", async () => {
  const root = scratchDirectory();
  try {
    writeManifest(
      root,
      "sick",
      HEALTH_ONLY.replace("name: healthy", "name: sick").replace(
        'check: ["node", "--version"]',
        'check: ["node", "-e", "process.exit(3)"]',
      ),
    );
    const result = await smokeRun({
      recipe: "sick",
      cwd: root,
      env: {},
      log: () => {},
    });
    assert.equal(result.ok, false);
    assert.match(result.detail, /exited 3/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("the fixture convention is what opts a recipe into the stronger replay mode", async () => {
  const root = scratchDirectory();
  try {
    writeFileSync(join(root, "runner.mjs"), "process.exit(0);\n");
    writeManifest(
      root,
      "fixtured",
      HEALTH_ONLY.replace(/healthy/g, "fixtured").replace(
        'args: ["-e", "process.exit(0)"]',
        'args: ["runner.mjs", "heal", "{spec}"]',
      ),
    );
    const manifestPath = join(root, "fixtured.toolspec.yaml");
    assert.equal(fixturePair(manifestPath), null);
    mkdirSync(join(root, "fixtures", "specs"), { recursive: true });
    mkdirSync(join(root, "fixtures", "sample-app"), { recursive: true });
    assert.equal(fixturePair(manifestPath), null); // specs directory still empty
    writeFileSync(join(root, "fixtures", "specs", "a.yaml"), "name: a\n");
    const pair = fixturePair(manifestPath);
    assert.equal(pair?.spec, join(root, "fixtures", "specs", "a.yaml"));

    const result = await smokeRun({
      recipe: "fixtured",
      cwd: root,
      // PATH only: enough to find `node`, and nothing that could steer a gate.
      env: { PATH: process.env.PATH },
      log: () => {},
    });
    assert.equal(result.mode, "replay");
    assert.equal(result.ok, true);
    assert.match(result.detail, /exited 0/);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

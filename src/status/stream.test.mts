import { test } from "node:test";
import assert from "node:assert/strict";
import {
  existsSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createStatusWriter } from "./stream.mts";

function scratchDir(): string {
  return mkdtempSync(join(tmpdir(), "formic-status-"));
}

test("each status kind has the declared NDJSON shape", () => {
  const dir = scratchDir();
  try {
    const statusFile = join(dir, "status.ndjson");
    const writer = createStatusWriter(statusFile);

    writer.emit({ event: "started" });
    writer.emit({ event: "progress" });
    writer.emit({ event: "ok" });
    writer.emit({ event: "failed" });
    writer.emit({ event: "needs-human" });

    const lines = readFileSync(statusFile, "utf8").trim().split("\n");
    const events = lines.map((line) => JSON.parse(line) as unknown);
    assert.deepEqual(events, [
      { event: "started" },
      { event: "progress" },
      { event: "ok" },
      { event: "failed" },
      { event: "needs-human" },
    ]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a missing status file is created and receives one line per event", () => {
  const dir = scratchDir();
  try {
    const statusFile = join(dir, "status.ndjson");
    const writer = createStatusWriter(statusFile);

    writer.emit({ event: "started" });
    writer.emit({ event: "ok" });

    assert.deepEqual(readFileSync(statusFile, "utf8").trim().split("\n"), [
      '{"event":"started"}',
      '{"event":"ok"}',
    ]);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("an existing status file is appended without replacing prior events", () => {
  const dir = scratchDir();
  try {
    const statusFile = join(dir, "status.ndjson");
    writeFileSync(statusFile, '{"event":"started"}\n');
    const writer = createStatusWriter(statusFile);

    writer.emit({ event: "progress" });

    assert.equal(
      readFileSync(statusFile, "utf8"),
      '{"event":"started"}\n{"event":"progress"}\n',
    );
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("a terminal event carries an evidence artifact pointer", () => {
  const dir = scratchDir();
  try {
    const statusFile = join(dir, "status.ndjson");
    const writer = createStatusWriter(statusFile);

    writer.emit({ event: "ok", artifact: "evidence/index.html" });

    const [line] = readFileSync(statusFile, "utf8").trim().split("\n");
    assert.deepEqual(JSON.parse(line), {
      event: "ok",
      artifact: "evidence/index.html",
    });
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

test("opt-out (no status file) never throws and creates nothing", () => {
  // statusFile is undefined, so there is no path to check directly — a candidate
  // path in a scratch dir stands in for "nowhere", and its non-existence is the
  // checkable claim, rather than merely asserting emit() doesn't throw.
  const dir = scratchDir();
  try {
    const wouldBeStatusFile = join(dir, "status.ndjson");
    const writer = createStatusWriter(undefined);
    writer.emit({ event: "started" });
    writer.emit({ event: "ok" });
    assert.equal(existsSync(wouldBeStatusFile), false);
    assert.deepEqual(readdirSync(dir), []);
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
});

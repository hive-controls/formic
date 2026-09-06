/**
 * `.env` writer + `.gitignore` coverage check, offline in a temp dir. Never reads
 * back a value with a raw `cat`-equivalent to assert a secret is ABSENT — the whole
 * point is asserting file bytes and process.env, not printing the secret.
 */
import { test, before, after, beforeEach } from "node:test";
import assert from "node:assert/strict";
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { envIsIgnored, upsertEnvVar } from "./credentials.mts";

let scratch: string;
before(() => {
  scratch = mkdtempSync(join(tmpdir(), "e2e-doctor-credentials-"));
});
after(() => {
  rmSync(scratch, { recursive: true, force: true });
});
beforeEach(() => {
  delete process.env.LAUNCHIE_TEST_KEY;
  delete process.env.LAUNCHIE_OTHER_KEY;
});

test("upsertEnvVar creates a new .env at mode 0600", () => {
  const dir = mkdtempSync(join(scratch, "create-"));
  const result = upsertEnvVar(dir, "LAUNCHIE_TEST_KEY", "abc123");
  assert.equal(result.action, "created");
  assert.equal(readFileSync(result.file, "utf8"), "LAUNCHIE_TEST_KEY=abc123\n");
  // NTFS has no POSIX mode bits — chmodSync(0o600) is a no-op there, so this
  // assertion only holds on a POSIX filesystem.
  if (process.platform !== "win32") {
    assert.equal(statSync(result.file).mode & 0o777, 0o600);
  }
  assert.equal(process.env.LAUNCHIE_TEST_KEY, "abc123");
});

test("upsertEnvVar replaces one line in place, the rest byte-identical", () => {
  const dir = mkdtempSync(join(scratch, "replace-"));
  const file = join(dir, ".env");
  writeFileSync(file, "FOO=1\nLAUNCHIE_TEST_KEY=old\nBAR=2\n");
  const result = upsertEnvVar(dir, "LAUNCHIE_TEST_KEY", "new");
  assert.equal(result.action, "replaced");
  assert.equal(
    readFileSync(file, "utf8"),
    "FOO=1\nLAUNCHIE_TEST_KEY=new\nBAR=2\n",
  );
  // NTFS has no POSIX mode bits — chmodSync(0o600) is a no-op there, so this
  // assertion only holds on a POSIX filesystem.
  if (process.platform !== "win32") {
    assert.equal(statSync(file).mode & 0o777, 0o600);
  }
});

test("upsertEnvVar appends when the name is absent", () => {
  const dir = mkdtempSync(join(scratch, "append-"));
  const file = join(dir, ".env");
  writeFileSync(file, "FOO=1\n");
  const result = upsertEnvVar(dir, "LAUNCHIE_OTHER_KEY", "v");
  assert.equal(result.action, "appended");
  assert.equal(readFileSync(file, "utf8"), "FOO=1\nLAUNCHIE_OTHER_KEY=v\n");
});

test("envIsIgnored: true when a .gitignore up the tree covers .env, false otherwise", () => {
  const ignored = mkdtempSync(join(scratch, "ignored-"));
  writeFileSync(join(ignored, ".gitignore"), "node_modules/\n.env\n");
  assert.equal(envIsIgnored(ignored), true);

  const notIgnored = mkdtempSync(join(scratch, "not-ignored-"));
  writeFileSync(join(notIgnored, ".gitignore"), "node_modules/\n");
  assert.equal(envIsIgnored(notIgnored), false);
});

/** Tests for the two leak paths a PR review caught in the Solari session lifecycle. */
import { test } from "node:test";
import assert from "node:assert/strict";
import { createReleaseGuard } from "./release-guard.mts";

test("releases once even when called concurrently", async () => {
  let closes = 0;
  let releases = 0;
  const release = createReleaseGuard(
    async () => {
      closes++;
    },
    async () => {
      releases++;
    },
  );
  await Promise.all([release(), release(), release()]);
  await release();
  assert.equal(closes, 1);
  assert.equal(releases, 1);
});

test("REVIEW REGRESSION — a failed release stays retryable", async () => {
  // The bug: `released = true` was set before the work succeeded, so one transient
  // failure made every later close()/fetchReplay() a no-op and stranded the session.
  let attempts = 0;
  const release = createReleaseGuard(
    async () => {},
    async () => {
      attempts++;
      if (attempts === 1) throw new Error("transient release failure");
    },
  );

  await assert.rejects(release(), /transient release failure/);
  await release(); // must actually retry, not silently no-op
  assert.equal(attempts, 2, "a failed release must be retryable");
});

test("REVIEW REGRESSION — the remote session is released even if closing the browser throws", async () => {
  // Releasing the remote session is the part that frees quota; a failing browser
  // shutdown must not skip it.
  let released = false;
  const release = createReleaseGuard(
    async () => {
      throw new Error("browser close failed");
    },
    async () => {
      released = true;
    },
  );
  await assert.rejects(release(), /browser close failed/);
  assert.ok(released, "releaseAndWait must still be attempted");
});

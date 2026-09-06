/** Preview liveness: a dead/expired preview is detected at the network edge, and a
 *  loopback host (the local gate's own server) is never probed at all. */
import { test } from "node:test";
import assert from "node:assert/strict";
import { checkPreviewLiveness } from "./preview-liveness.mts";

const neverCalled = () => {
  throw new Error("fetch must not be called");
};

test("a loopback URL is alive by definition — no network probe", async () => {
  for (const url of [
    "http://127.0.0.1:8080/",
    "http://localhost:3000/app",
    "http://[::1]:8080/",
  ]) {
    const liveness = await checkPreviewLiveness(url, neverCalled);
    assert.deepEqual(liveness, { alive: true, status: null }, url);
  }
});

test("a 2xx from the preview means alive", async () => {
  const liveness = await checkPreviewLiveness(
    "https://sbx-1-8080.preview.getsolari.com/?pt_token=x",
    async () => ({ status: 200 }),
  );
  assert.deepEqual(liveness, { alive: true, status: 200 });
});

test("a 404 from the preview means dead, and the status is reported", async () => {
  const liveness = await checkPreviewLiveness(
    "https://sbx-1-8080.preview.getsolari.com/",
    async () => ({ status: 404 }),
  );
  assert.deepEqual(liveness, { alive: false, status: 404 });
});

test("a probe that cannot reach the host at all means dead, with no status", async () => {
  const liveness = await checkPreviewLiveness(
    "https://sbx-1-8080.preview.getsolari.com/",
    async () => {
      throw new Error("fetch failed");
    },
  );
  assert.deepEqual(liveness, { alive: false, status: null });
});

test("a 302 with redirect: manual means the host is serving — alive", async () => {
  let seenInit: { redirect?: string } | undefined;
  const liveness = await checkPreviewLiveness(
    "https://sbx-1-8080.preview.getsolari.com/",
    async (_url, init) => {
      seenInit = init;
      return { status: 302 };
    },
  );
  assert.deepEqual(liveness, { alive: true, status: 302 });
  assert.equal(seenInit?.redirect, "manual", "redirects are not followed");
});

test("a 401 means dead — the gate refused the preview", async () => {
  const liveness = await checkPreviewLiveness(
    "https://sbx-1-8080.preview.getsolari.com/",
    async () => ({ status: 401 }),
  );
  assert.deepEqual(liveness, { alive: false, status: 401 });
});

test("a probe that times out counts as dead", async () => {
  const liveness = await checkPreviewLiveness(
    "https://sbx-1-8080.preview.getsolari.com/",
    async (_url, init) => {
      assert.ok(init?.signal instanceof AbortSignal, "a timeout signal is set");
      throw new DOMException("The operation timed out", "TimeoutError");
    },
  );
  assert.deepEqual(liveness, { alive: false, status: null });
});

test("any 127.0.0.0/8 or v4-mapped loopback address is exempt, not just 127.0.0.1", async () => {
  for (const url of [
    "http://127.0.0.2:9000/",
    "http://127.53.0.1/",
    "http://[::ffff:127.0.0.1]:8080/",
  ]) {
    const liveness = await checkPreviewLiveness(url, neverCalled);
    assert.deepEqual(liveness, { alive: true, status: null }, url);
  }
});

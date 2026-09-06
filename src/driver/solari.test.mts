/**
 * The CDP-setup-failure path of the Solari backend, offline.
 *
 * PR #4 fixed the leak (a live, billing session was abandoned when connectOverCDP
 * failed after create()) but could not test it because the driver built its client
 * internally. The client and the CDP connector are now injectable for exactly this.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import type { Browser } from "playwright-core";
import { SolariDriver, type SolariClientLike } from "./solari.mts";

function fakeClient(releaseBehaviour: () => Promise<void>) {
  const calls = { create: 0, release: 0, close: 0 };
  const releasedIds: string[] = [];
  const client: SolariClientLike = {
    sessions: {
      async create() {
        calls.create++;
        return {
          id: `sess_test_${calls.create}`,
          cdpEndpoint: "ws://unreachable",
        };
      },
      async releaseAndWait(id: string) {
        calls.release++;
        releasedIds.push(id);
        await releaseBehaviour();
      },
      async downloadReplay() {
        return new Uint8Array();
      },
    },
    async close() {
      calls.close++;
    },
  };
  return { client, calls, releasedIds };
}

test("a persistent CDP connect failure retries once with a new session, then gives up — releasing both", async () => {
  const { client, calls, releasedIds } = fakeClient(async () => {});
  const driver = new SolariDriver({
    apiKey: "k",
    client,
    connectOverCDP: async () => {
      throw new Error("cdp refused");
    },
  });
  await assert.rejects(driver.open(), /connectOverCDP failed twice/);
  assert.equal(calls.create, 2, "a fresh session is created for the retry");
  assert.equal(calls.release, 2, "both sessions must be released");
  assert.deepEqual(releasedIds, ["sess_test_1", "sess_test_2"]);
  assert.equal(
    calls.close,
    1,
    "the SDK client is closed once retries are exhausted",
  );
});

test("a create() failure during the retry still closes the SDK client, after releasing the first session", async () => {
  const calls = { create: 0, release: 0, close: 0 };
  const releasedIds: string[] = [];
  const client: SolariClientLike = {
    sessions: {
      async create() {
        calls.create++;
        if (calls.create === 2) {
          throw new Error("create() refused (retry)");
        }
        return {
          id: `sess_test_${calls.create}`,
          cdpEndpoint: "ws://unreachable",
        };
      },
      async releaseAndWait(id: string) {
        calls.release++;
        releasedIds.push(id);
      },
      async downloadReplay() {
        return new Uint8Array();
      },
    },
    async close() {
      calls.close++;
    },
  };
  const driver = new SolariDriver({
    apiKey: "k",
    client,
    connectOverCDP: async () => {
      throw new Error("cdp refused");
    },
  });
  await assert.rejects(driver.open(), /create\(\) refused \(retry\)/);
  assert.deepEqual(
    releasedIds,
    ["sess_test_1"],
    "only the first session ever existed to release",
  );
  assert.equal(
    calls.close,
    1,
    "the SDK client is closed even though the retry's create() itself failed",
  );
});

test("a create() failure on the very first attempt still closes the SDK client — nothing existed to release", async () => {
  const calls = { create: 0, release: 0, close: 0 };
  const client: SolariClientLike = {
    sessions: {
      async create() {
        calls.create++;
        throw new Error("create() refused");
      },
      async releaseAndWait() {
        calls.release++;
      },
      async downloadReplay() {
        return new Uint8Array();
      },
    },
    async close() {
      calls.close++;
    },
  };
  const driver = new SolariDriver({
    apiKey: "k",
    client,
    connectOverCDP: async () => fakeBrowser(),
  });
  await assert.rejects(driver.open(), /create\(\) refused/);
  assert.equal(calls.create, 1);
  assert.equal(
    calls.release,
    0,
    "nothing to release — no session was ever created",
  );
  assert.equal(
    calls.close,
    1,
    "the SDK client is closed even though create() itself failed",
  );
});

test("REVIEW REGRESSION (re-review P2) — a failed release on a connect failure is surfaced immediately, no retry", async () => {
  // If release also fails, open() never returns a session, so nothing downstream can
  // retry it — and a bigger error is already in flight, so this attempt must not be
  // silently retried with yet another session while the first may still be live.
  const { client, calls } = fakeClient(async () => {
    throw new Error("release timed out");
  });
  const driver = new SolariDriver({
    apiKey: "k",
    client,
    connectOverCDP: async () => {
      throw new Error("cdp refused");
    },
  });
  await assert.rejects(
    driver.open(),
    (err: unknown) =>
      err instanceof AggregateError &&
      /sess_test_1/.test(err.message) &&
      err.errors.some((e) => /cdp refused/.test((e as Error).message)) &&
      err.errors.some((e) => /release timed out/.test((e as Error).message)),
  );
  assert.equal(calls.create, 1, "a release failure must not trigger a retry");
  assert.equal(calls.release, 1);
});

test("a CDP connect that times out once succeeds on retry — the first session is released, the record carries latency + retried=true", async () => {
  const { client, calls, releasedIds } = fakeClient(async () => {});
  let connectCalls = 0;
  const driver = new SolariDriver({
    apiKey: "k",
    client,
    connectOverCDP: async () => {
      connectCalls++;
      if (connectCalls === 1) {
        throw new Error(
          "Timeout 30000ms exceeded connecting to ws://unreachable",
        );
      }
      return fakeBrowser();
    },
  });
  const session = await driver.open();
  assert.equal(calls.create, 2, "the retry opens a brand-new session");
  assert.deepEqual(
    releasedIds,
    ["sess_test_1"],
    "only the first (failed) session is released",
  );
  assert.equal(session.sessionId, "sess_test_2", "the second session is kept");
  assert.ok(session.cdpConnect, "connect diagnostics are recorded");
  assert.equal(session.cdpConnect!.retried, true);
  assert.equal(typeof session.cdpConnect!.latencyMs, "number");
  assert.ok(session.cdpConnect!.latencyMs >= 0);
  await session.close();
  assert.deepEqual(
    releasedIds,
    ["sess_test_1", "sess_test_2"],
    "close() releases the kept session too, never re-releasing the first",
  );
  assert.equal(calls.release, 2);
});

/** A connected browser with one context and one page, as connectOverCDP returns. */
function fakeBrowser(): Browser {
  return {
    contexts: () => [{ pages: () => [{}] }],
    close: async () => {},
  } as unknown as Browser;
}

test("MEASURED — closing a session also closes the SDK client, or its local proxy server keeps node alive forever", async () => {
  // The SDK's `new Solari()` starts a LocalProxy HTTP server on 127.0.0.1 that only
  // `solari.close()` stops. Measured: after browser.close() + releaseAndWait() a
  // TCPServerWrap handle survived and every Solari-gated run hung at exit until killed.
  const { client, calls } = fakeClient(async () => {});
  const driver = new SolariDriver({
    apiKey: "k",
    client,
    connectOverCDP: async () => fakeBrowser(),
  });
  const session = await driver.open();
  await session.close();
  assert.equal(calls.release, 1);
  assert.equal(calls.close, 1, "the SDK client must be closed on release");
});

test("MEASURED — a session's initial context has no pages; the page must come from browser.newPage(), never context.newPage()", async () => {
  // Three samples, 2026-09-01: a page created with context.newPage() inside the session's
  // initial (empty) context never produced a replay when its first navigation failed —
  // 404 forty minutes later — while browser.newPage() (a fresh context) yielded one in
  // ~6 s for the identical refused navigation. Successful navigations recorded either way.
  const { client } = fakeClient(async () => {});
  const calls = { contextNewPage: 0, browserNewPage: 0 };
  const browser = {
    contexts: () => [
      {
        pages: () => [],
        newPage: async () => {
          calls.contextNewPage++;
          return {};
        },
      },
    ],
    newPage: async () => {
      calls.browserNewPage++;
      return {};
    },
    close: async () => {},
  } as unknown as Browser;
  const driver = new SolariDriver({
    apiKey: "k",
    client,
    connectOverCDP: async () => browser,
  });
  const session = await driver.open();
  await session.close();
  assert.equal(calls.contextNewPage, 0, "never a page in the initial context");
  assert.equal(
    calls.browserNewPage,
    1,
    "the page comes from browser.newPage()",
  );
});

/** Solari cloud-browser backend. */
import { chromium, type Browser } from "playwright-core";
import { Solari } from "@solarisdk/browser";
import type { Driver, DriverSession } from "./types.mts";
import { parseReplay } from "../evidence/segment.mts";
import { createReleaseGuard } from "./release-guard.mts";

/** The slice of the Solari client this driver uses — injectable so the failure paths
 *  around a live, billing session are testable with no key and no network. */
export interface SolariClientLike {
  sessions: {
    create(options: {
      recording: boolean;
      stealth: boolean;
    }): Promise<{ id: string; cdpEndpoint: string }>;
    releaseAndWait(id: string): Promise<unknown>;
    downloadReplay(id: string): Promise<Uint8Array | ArrayBuffer>;
  };
  /** Stops the SDK's LocalProxy server. Optional only for test doubles: the real client
   *  starts that server on construction, and node cannot exit while it listens. */
  close?(): Promise<void>;
}

export interface SolariDriverOptions {
  apiKey: string;
  stealth?: boolean;
  /** Test seam; defaults to the real SDK client. */
  client?: SolariClientLike;
  /** Test seam; defaults to Playwright's connectOverCDP. */
  connectOverCDP?: (cdpEndpoint: string) => Promise<Browser>;
}

/** Replay is not ready the instant a session is released. Measured: 4-6 s in the recording
 *  probe; 2.4-10.8 s across six controlled sessions on 2026-09-01. A 404 means "pending".
 *  The window is ~30 s (20 attempts × 1.5 s) — three times the slowest arrival seen. */
const REPLAY_ATTEMPTS = 20;
const REPLAY_BACKOFF_MS = 1500;

/** The outcome of one session-create + connectOverCDP attempt. A connect failure whose
 *  release ALSO fails throws immediately (below) rather than being returned — a session
 *  that might still be live must not be abandoned to open yet another one on retry. */
type ConnectAttempt =
  | {
      ok: true;
      session: { id: string; cdpEndpoint: string };
      browser: Browser;
      latencyMs: number;
    }
  | { ok: false; sessionId: string; connectError: Error };

export class SolariDriver implements Driver {
  readonly name = "solari-browser";
  readonly canRecord = true;

  constructor(private readonly options: SolariDriverOptions) {}

  /** Creates one session and connects over CDP, timing the connect. On a connect
   *  failure the session is released before returning so the caller can safely retry
   *  with a new one — unless the release itself also fails, in which case this throws
   *  immediately (never swallowed: nothing downstream could retry that release either). */
  private async connectSession(
    solari: SolariClientLike,
    connect: (endpoint: string) => Promise<Browser>,
  ): Promise<ConnectAttempt> {
    const session = await solari.sessions.create({
      recording: true,
      stealth: this.options.stealth ?? false,
    });
    const connectStartedAt = Date.now();
    try {
      const browser = await connect(session.cdpEndpoint);
      return {
        ok: true,
        session,
        browser,
        latencyMs: Date.now() - connectStartedAt,
      };
    } catch (connectError) {
      try {
        await solari.sessions.releaseAndWait(session.id);
      } catch (releaseError) {
        // The client is closed by open()'s single centralized handler, once, on any
        // failure path that reaches it — not here, to avoid closing it twice.
        throw new AggregateError(
          [connectError, releaseError],
          `CDP setup failed AND session ${session.id} could not be released — it is still live`,
        );
      }
      return {
        ok: false,
        sessionId: session.id,
        connectError: connectError as Error,
      };
    }
  }

  /** Exactly one retry with a NEW session — the SDK's LocalProxy times out
   *  intermittently and the cause (proxy-side, session-boot-side, transient network)
   *  is not discriminated, so a blanket retry is the whole fix. Logs both session ids
   *  through the existing console path so the flake rate is visible in run output. */
  private async connectWithOneRetry(
    solari: SolariClientLike,
    connect: (endpoint: string) => Promise<Browser>,
  ): Promise<{
    session: { id: string; cdpEndpoint: string };
    browser: Browser;
    latencyMs: number;
    retried: boolean;
  }> {
    const first = await this.connectSession(solari, connect);
    if (first.ok) return { ...first, retried: false };

    console.log(
      `session ${first.sessionId} failed to connect over CDP (${first.connectError.message}) — released; retrying with a new session`,
    );
    const second = await this.connectSession(solari, connect);
    if (second.ok) {
      console.log(
        `session ${second.session.id} connected on retry (first attempt was session ${first.sessionId})`,
      );
      return { ...second, retried: true };
    }

    // The client is closed by open()'s single centralized handler, once, on any
    // failure path that reaches it — not here, to avoid closing it twice.
    throw new AggregateError(
      [first.connectError, second.connectError],
      `connectOverCDP failed twice — session ${first.sessionId} and session ${second.sessionId} were both released`,
    );
  }

  async open(): Promise<DriverSession> {
    const solari: SolariClientLike =
      this.options.client ?? new Solari({ apiKey: this.options.apiKey });
    const connect =
      this.options.connectOverCDP ??
      ((endpoint: string) => chromium.connectOverCDP(endpoint));

    // Single centralized close: ANY failure past this point — the very first
    // create(), a connect failure, the retry's create(), or the retry's connect —
    // releases whatever session exists (connectWithOneRetry's own job) and then
    // closes the SDK client exactly once, here, rather than at each of those
    // scattered failure sites.
    let connected: {
      session: { id: string; cdpEndpoint: string };
      browser: Browser;
      latencyMs: number;
      retried: boolean;
    };
    try {
      connected = await this.connectWithOneRetry(solari, connect);
    } catch (error) {
      await solari.close?.().catch(() => {});
      throw error;
    }
    const {
      session,
      browser: connectedBrowser,
      latencyMs: cdpConnectLatencyMs,
      retried: cdpConnectRetried,
    } = connected;

    // If anything after this point fails, the remote session is already live and
    // billing. Release it before rethrowing or a transient failure leaks a session
    // against the plan's concurrency cap until it expires.
    let page;
    try {
      // A session starts with one context and NO pages. Measured (3 samples): a page
      // created with context.newPage() inside that initial context is not recorded until
      // it loads a document — a run whose first navigation fails then never gets a replay
      // (404 forty minutes later). browser.newPage() opens a fresh context that records
      // from the start, for the identical refused navigation. Reuse a page only if one
      // already exists.
      page =
        connectedBrowser.contexts()[0]?.pages()[0] ??
        (await connectedBrowser.newPage());
    } catch (setupError) {
      await connectedBrowser.close().catch(() => {});
      try {
        await solari.sessions.releaseAndWait(session.id);
      } catch (releaseError) {
        // open() never returns a session on this path, so nothing downstream can retry
        // the release. Swallowing this would hide that a paid session is still live.
        throw new AggregateError(
          [setupError, releaseError],
          `CDP setup failed AND session ${session.id} could not be released — it is still live`,
        );
      } finally {
        // A bigger error is already in flight on this path; a failed proxy stop must not
        // mask it, and the process will still exit once the caller's error surfaces.
        await solari.close?.().catch(() => {});
      }
      throw setupError;
    }

    const release = createReleaseGuard(
      () => connectedBrowser.close(),
      async () => {
        try {
          await solari.sessions.releaseAndWait(session.id);
        } finally {
          // Measured: without this a TCPServerWrap (the SDK's LocalProxy) survives every
          // clean release and the process hangs at exit until killed.
          await solari.close?.();
        }
      },
    );

    return {
      sessionId: session.id,
      page,
      cdpConnect: {
        latencyMs: cdpConnectLatencyMs,
        retried: cdpConnectRetried,
      },
      async fetchReplay() {
        // The stream only exists once the session is released.
        await release();
        for (let attempt = 1; attempt <= REPLAY_ATTEMPTS; attempt++) {
          try {
            const bytes = await solari.sessions.downloadReplay(session.id);
            const text = Buffer.from(
              bytes instanceof ArrayBuffer ? new Uint8Array(bytes) : bytes,
            ).toString("utf8");
            return parseReplay(text);
          } catch {
            if (attempt === REPLAY_ATTEMPTS)
              throw new Error(
                `replay never became available for ${session.id}`,
              );
            await new Promise((resolve) =>
              setTimeout(resolve, REPLAY_BACKOFF_MS),
            );
          }
        }
        return null;
      },
      close: release,
    };
  }
}

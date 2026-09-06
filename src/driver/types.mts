/**
 * The driver seam.
 *
 * Non-negotiable: Solari is ONE backend among peers, never the
 * foundation. The Solari probes made the seam cheap — a Solari session exposes a CDP endpoint, so
 * both backends hand back an ordinary Playwright `Page` and the harness above this line
 * never learns which one it is talking to.
 */
import type { Page } from "playwright-core";
import type { ReplayEvent } from "../evidence/types.mts";

export interface DriverSession {
  /** Opaque id for the audit record. */
  readonly sessionId: string;
  readonly page: Page;
  /** Present only for a backend that connects over CDP (Solari). `retried` is true when
   *  the first connect attempt failed and a fresh session's connect succeeded. */
  readonly cdpConnect?: { latencyMs: number; retried: boolean };
  /**
   * The replay stream for this session, or `null` when the backend cannot record.
   *
   * A `null` here is a capability gap, not an error — but it means no evidence
   * artifact, so a backend that returns `null` must never be used for a run whose
   * output is a repair PR. Both shipped backends record: Solari server-side, the local
   * driver via injected rrweb (driver/rrweb-recorder.mts) — so Solari is not
   * load-bearing for evidence, which is the structural dependence the seam forbids.
   */
  fetchReplay(): Promise<ReplayEvent[] | null>;
  close(): Promise<void>;
}

export interface Driver {
  /** Backend name, recorded in the audit trail so a run is attributable. */
  readonly name: string;
  /** Whether this backend can produce an evidence stream at all. */
  readonly canRecord: boolean;
  open(): Promise<DriverSession>;
}

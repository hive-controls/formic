/**
 * A pre-attempt liveness probe for the app under heal. An expired Solari preview
 * sandbox 404s between the initial run and the first healer attempt, and an attempt
 * spent diagnosing that is an attempt spent on an environment fault — the heal loop
 * probes before each attempt and fails fast instead (see loop.mts).
 *
 * The seam is the fetch: tests inject a stub, production gets the global fetch.
 * Loopback hosts are the local gate's own server — exempt, mirroring the redaction
 * class in replay/evidence.mts, so a local heal is never blocked by a probe.
 */
import { isLoopbackHost } from "../replay/evidence.mts";

export interface PreviewLiveness {
  alive: boolean;
  /** The HTTP status when the host answered at all; null when it could not be reached
   *  (or when no probe was needed — a loopback host). */
  status: number | null;
}

export type PreviewLivenessFetch = (
  url: string,
  init?: { redirect: "manual"; signal: AbortSignal },
) => Promise<{ status: number }>;

export type PreviewLivenessCheck = (url: string) => Promise<PreviewLiveness>;

export async function checkPreviewLiveness(
  url: string,
  fetchImpl: PreviewLivenessFetch = (target, init) => fetch(target, init),
  timeoutMs = 5000,
): Promise<PreviewLiveness> {
  let hostname: string;
  try {
    // Node's URL keeps the brackets on an IPv6 hostname ("[::1]").
    hostname = new URL(url).hostname.replace(/^\[|\]$/g, "");
  } catch {
    // A URL that does not parse tells the probe nothing — do not block healing on it.
    return { alive: true, status: null };
  }
  if (isLoopbackHost(hostname)) return { alive: true, status: null };
  try {
    // redirect: "manual" — a redirect means the host IS serving (alive); following it
    // would only cost time and could leak the tokened URL to the redirect target.
    const response = await fetchImpl(url, {
      redirect: "manual",
      signal: AbortSignal.timeout(timeoutMs),
    });
    return {
      alive: response.status >= 200 && response.status < 400,
      status: response.status,
    };
  } catch {
    // Unreachable AND timed out both land here: dead either way.
    return { alive: false, status: null };
  }
}

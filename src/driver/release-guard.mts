/**
 * Single-flight release guard for a remote session.
 *
 * Extracted from SolariDriver so this logic is testable without a network round trip —
 * it is subtle in two ways a review caught, and both leak paid remote sessions:
 *
 *  1. The guard must be the in-flight promise, NOT a boolean set before the work
 *     succeeds. A flag flipped up front turns a transient release failure into a
 *     permanently un-retryable no-op, stranding the session until it expires.
 *  2. Releasing the remote session must happen even if closing the browser throws —
 *     the remote release is the part that actually frees quota.
 */
export function createReleaseGuard(
  closeBrowser: () => Promise<void>,
  releaseSession: () => Promise<void>,
): () => Promise<void> {
  let inFlight: Promise<void> | null = null;

  return function release(): Promise<void> {
    inFlight ??= (async () => {
      try {
        await closeBrowser();
      } finally {
        await releaseSession();
      }
    })().catch((error: unknown) => {
      // Clear the guard so a later close()/fetchReplay() can retry the release.
      inFlight = null;
      throw error;
    });
    return inFlight;
  };
}

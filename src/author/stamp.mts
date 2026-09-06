/**
 * The one clock authoring and import read.
 *
 * Authoring is a build step, not a recording: the same input must produce the same bytes,
 * or a spec cannot be regenerated and diffed against the copy in the repo. Step ids are
 * derived from position (`./index.mts`), which leaves `capturedAt` as the only remaining
 * source of drift — so it honours `SOURCE_DATE_EPOCH`, the same environment variable
 * reproducible builds use elsewhere, and falls back to the wall clock when nothing asks
 * for reproducibility.
 *
 * A capture is the opposite case and keeps its wall clock: it records when a real session
 * happened, and that timestamp is evidence.
 */

/** ISO-8601 stamp for an authored or imported spec: `SOURCE_DATE_EPOCH` (seconds since
 *  the epoch) when it is set to a value that is actually a date, else the wall clock. An
 *  unusable value warns and falls back rather than throwing — a broken environment
 *  variable must not stop someone authoring a spec, but it must not pass unnoticed either,
 *  because the whole point of setting it is that the output be reproducible. */
export function authoredCapturedAt(
  env: NodeJS.ProcessEnv = process.env,
  warn: (message: string) => void = console.error,
): string {
  const raw = env.SOURCE_DATE_EPOCH;
  if (raw !== undefined && raw.trim() !== "") {
    const stamp = isoFromEpochSeconds(raw.trim());
    if (stamp !== undefined) return stamp;
    warn(
      `SOURCE_DATE_EPOCH is not a usable epoch second count (${raw.trim()}) — using the wall clock, so this output is not reproducible`,
    );
  }
  return new Date().toISOString();
}

/** The ISO stamp for a decimal epoch-second string, or undefined when it is not one or
 *  lands outside the range a Date can represent (±8.64e15 ms). `toISOString` throws on an
 *  out-of-range Date, so the range is checked here rather than caught there. */
function isoFromEpochSeconds(raw: string): string | undefined {
  if (!/^\d+$/.test(raw)) return undefined;
  const milliseconds = Number(raw) * 1000;
  if (!Number.isFinite(milliseconds) || Math.abs(milliseconds) > 8.64e15) {
    return undefined;
  }
  return new Date(milliseconds).toISOString();
}

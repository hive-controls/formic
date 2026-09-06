/**
 * Spec rebasing — swap the origin recorded at capture time for the origin a run's host
 * is actually serving from, without losing that host's own path/query shape.
 *
 * A capture always targets a fixed dev server (its `startUrl` origin); a run's host may
 * not be that address at all — a Solari sandbox's preview URL can carry a routing prefix
 * and/or an access token in its path or query. `rebaseSpec`/`unrebaseSpec` act ONLY on
 * `startUrl` and `goto` targets whose origin equals the spec's captured origin — every
 * other target (a different origin, or a target the URL parser rejects) passes through
 * unchanged, mirroring fleet.mts's malformed-URL handling (try `new URL()`, catch, skip).
 *
 * Forward (`rebaseSpec`): new origin = the host's origin; new pathname = the host's own
 * pathname (its routing/token prefix, trailing slash trimmed) + the captured pathname
 * (no `//` join — the prefix is trimmed before concatenation, and a captured pathname
 * always starts with `/`); new search = the captured query with the host's own query
 * params overlaid (the host wins on a key collision — that is how a query-token shape
 * survives a captured `?page=2`); new hash = the host's hash (a capture's own hash does
 * not survive — none of the previewUrl shapes measured so far carry one).
 *
 * Inverse (`unrebaseSpec`): strips exactly the prefix and query keys `rebaseSpec` added
 * and restores the original origin, so a repaired spec written back to disk — or diffed
 * against the pristine one — never carries a rebased origin or a live token.
 *
 * Measured previewUrl shape (probes/host-app-in-solari.mts against
 * usecases/self-healing-e2e/sample-app, 2026-09-02, sandbox killed after): a QUERY-token
 * shape — `https://<sandbox-id>-<port>.preview.getsolari.com?pt_token=<token>` (scheme
 * https, host `<sandbox-id>-<port>.preview.getsolari.com`, no path segment before the
 * `?` — `new URL()` still normalizes that to pathname `/`, so this is the same shape as
 * the bare-root query-token case below), query param name `pt_token`. See the rebase
 * test using this exact (redacted) shape.
 */
import type { Spec } from "../spec/types.mts";

function parseUrl(value: string): URL | undefined {
  try {
    return new URL(value);
  } catch {
    return undefined;
  }
}

function trimTrailingSlash(pathname: string): string {
  return pathname.endsWith("/") ? pathname.slice(0, -1) : pathname;
}

function rebaseUrl(target: string, capturedOrigin: string, base: URL): string {
  const url = parseUrl(target);
  if (!url || url.origin !== capturedOrigin) return target;
  const merged = new URLSearchParams(url.search);
  for (const [key, value] of base.searchParams) merged.set(key, value);
  const rebased = new URL(base.href);
  rebased.pathname = trimTrailingSlash(base.pathname) + url.pathname;
  rebased.search = merged.toString();
  rebased.hash = base.hash;
  return rebased.toString();
}

function unrebaseUrl(
  target: string,
  base: URL,
  originalOrigin: string,
): string {
  const url = parseUrl(target);
  if (!url || url.origin !== base.origin) return target;
  const prefix = trimTrailingSlash(base.pathname);
  let pathname = url.pathname;
  if (prefix && pathname.startsWith(prefix)) {
    pathname = pathname.slice(prefix.length) || "/";
  }
  const merged = new URLSearchParams(url.search);
  for (const [key] of base.searchParams) merged.delete(key);
  const restored = new URL(originalOrigin);
  restored.pathname = pathname;
  restored.search = merged.toString();
  return restored.toString();
}

function swapTargets(spec: Spec, swap: (target: string) => string): Spec {
  return {
    ...spec,
    startUrl: swap(spec.startUrl),
    steps: spec.steps.map((step) =>
      step.action === "goto" && step.target !== undefined
        ? { ...step, target: swap(step.target) }
        : step,
    ),
  };
}

/** Rebase every `startUrl`/`goto` target that shares the spec's captured origin onto
 *  `toBase` (a host's `baseUrl`). Everything else passes through unchanged. */
export function rebaseSpec(spec: Spec, toBase: string): Spec {
  const base = parseUrl(toBase);
  const capturedOrigin = parseUrl(spec.startUrl)?.origin;
  if (!base || !capturedOrigin) return spec;
  return swapTargets(spec, (target) => rebaseUrl(target, capturedOrigin, base));
}

/** Undo `rebaseSpec`: restore `originalOrigin` on every target rebased onto `fromBase`
 *  (a host's `baseUrl`), stripping the prefix and query keys it added. */
export function unrebaseSpec(
  spec: Spec,
  fromBase: string,
  originalOrigin: string,
): Spec {
  const base = parseUrl(fromBase);
  if (!base) return spec;
  return swapTargets(spec, (target) =>
    unrebaseUrl(target, base, originalOrigin),
  );
}

/** The `--app` host a spec was rebased onto, for undoing it everywhere afterwards. */
export interface AppHostRebase {
  /** The host's baseUrl the run used (may carry a token). */
  baseUrl: string;
  /** The origin the spec was captured against. */
  originalOrigin: string;
}

/** Undo `rebaseSpec` on free text — evidence JSON, the inlined evidence page, rrweb
 *  events — where the host's URL appears verbatim (an `href`, a resource `src`, an error
 *  message). Every URL on `fromBase`'s origin is rewritten through the same inverse the
 *  spec uses; anything on another origin, and any text that is not a URL, passes through
 *  untouched. Text-level on purpose: rrweb events embed absolute URLs in shapes no parser
 *  here should have to know about. */
export function unrebaseText(
  text: string,
  fromBase: string,
  originalOrigin: string,
): string {
  const base = parseUrl(fromBase);
  if (!base) return text;
  const escapedOrigin = base.origin.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&");
  // A URL ends at whitespace, a quote, a bracket of any kind, or a JSON/JS escape;
  // trailing sentence punctuation (`see <url>.`) belongs to the text, not the URL.
  const hostedUrl = new RegExp(
    `${escapedOrigin}[^\\s"'<>\\\\()\\[\\]{}]*`,
    "g",
  );
  return text.replace(hostedUrl, (match) => {
    const trailing = /[.,;:!?]+$/.exec(match)?.[0] ?? "";
    const url = match.slice(0, match.length - trailing.length);
    const restored = unrebaseUrl(url, base, originalOrigin);
    // A bare origin (no path) unrebases to `<origin>/`; keep the bare form the text had.
    return (
      (url === base.origin ? restored.replace(/\/$/, "") : restored) + trailing
    );
  });
}

/**
 * Evidence assembly: a replay result + the session's replay stream → the audit record.
 *
 * Pure and deterministic given its inputs. The only non-determinism is the decision id,
 * which is minted here because a reviewer cites it and it must be unique per run.
 *
 * A segment that cannot render is refused, not persisted. The whole product is
 * "reviewable evidence"; an artifact that looks like evidence and shows nothing is the
 * failure mode this repo keeps finding (see the probe findings), so the
 * check sits on the only path that writes an audit record.
 */
import { unrebaseText, type AppHostRebase } from "../host/rebase.mts";
import { createHash, randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type {
  EvidenceRecord,
  ReplayEvent,
  ReplaySegment,
} from "../evidence/types.mts";
import { isRenderable, sliceSegments } from "../evidence/segment.mts";
import type { ReplayResult } from "./runner.mts";

export class EvidenceIntegrityError extends Error {
  constructor(readonly segment: ReplaySegment) {
    super(
      `segment for step ${segment.stepIndex} (${segment.stepId}) is not renderable — ` +
        "refusing to persist evidence that cannot be reviewed",
    );
    this.name = "EvidenceIntegrityError";
  }
}

export interface EvidenceMeta {
  /** Driver name — which backend produced the run (audit attribution). */
  driver: string;
  /** DriverSession.sessionId — converted to a stable, non-identifying reference before
   *  it reaches the audit record. Absent only for callers with no session. */
  sessionId?: string;
  /** Defaults to the harness package version, `+<FORMIC_GIT_SHA>` when set. */
  systemVersion?: string;
  /** `null` on the token-free path. The heal loop sets it when a model acted. */
  modelVersion?: string | null;
  /** Set by a caller that hosted the app itself (`--app`). Already redacted — see
   *  `redactPreviewUrl`. */
  host?: { name: string; kind: "Outside" | "Inside"; baseUrl: string } | null;
  decisionId?: string;
  timestamp?: string;
  /** Every value this run resolved from the environment (ReplayResult.resolvedSecrets).
   *  Applied by the one scrub pass at the bottom of `assembleEvidence`, so the record —
   *  step log, failure text, snapshots and replay segments alike — is clean before any
   *  writer touches it. */
  secrets?: ResolvedSecret[];
  /** Mirrors DriverSession.cdpConnect — forwarded verbatim by a caller that has the
   *  session, so the flake rate is measurable from the audit trail. */
  cdpConnect?: { latencyMs: number; retried: boolean };
}

const SESSION_REFERENCE_HEX_LENGTH = 32;

/** A deterministic correlation handle that never publishes the backend's raw,
 *  opaque session id. Hashing the whole value preserves equality checks without
 *  retaining any component of the identity, and without assuming anything about
 *  the id's internal structure — an opaque id is never parsed, only matched whole.
 *  ALWAYS hashes — a raw id is never passed through even when it already looks like a
 *  reference (`session_<hex>`): a distinct backend identity that happens to collide
 *  with that shape would otherwise be published verbatim instead of hashed. Every
 *  caller applies this exactly once, at assembly time (assembleEvidence,
 *  scrubBackendIdentity) — nothing downstream re-hashes an already-final reference,
 *  so a second, accidental application is not a correctness concern this needs to
 *  guard against on its own. 128 bits (32 hex chars) of the digest, not 64: this is
 *  the one identifier this repo intentionally cannot reverse or recompute, so its
 *  collision resistance should not be cut for display width the way a decision id's
 *  short human-facing prefix rightly is (decisionId is unique by random generation,
 *  not collision-resistant hashing — a fundamentally different guarantee). */
export function sessionReference(
  sessionId: string | null | undefined,
): string | null {
  // Absent, not merely a value to hash: an empty string carries no identity to
  // reference, and hashing it would mint a reference for a session that was never
  // there (the single guard every caller of this function relies on).
  if (!sessionId) return null;
  const digest = createHash("sha256").update(sessionId).digest("hex");
  return `session_${digest.slice(0, SESSION_REFERENCE_HEX_LENGTH)}`;
}

/** A URL's hostname, or `undefined` for malformed input — mirrors this file's other
 *  `new URL()` try/catch handling. */
export function hostnameOf(url: string): string | undefined {
  try {
    return new URL(url).hostname;
  } catch {
    return undefined;
  }
}

function walkStrings(
  value: unknown,
  transform: (text: string) => string,
): unknown {
  if (typeof value === "string") return transform(value);
  if (Array.isArray(value)) {
    return value.map((item) => walkStrings(item, transform));
  }
  if (value !== null && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>).map(([key, item]) => [
        key,
        walkStrings(item, transform),
      ]),
    );
  }
  return value;
}

export interface BackendIdentity {
  /** Raw backend session id(s) known at scrub time, verbatim — each becomes its own
   *  stable reference (sessionReference) wherever it EXACTLY appears, including a
   *  `sessionId` field itself, which this pass treats as just another string.
   *  Session ids are OPAQUE (DriverSession.sessionId, driver/types.mts) — never
   *  parsed for structure. A colon, a hyphen, or any other character inside one is
   *  part of the opaque value, not evidence of an embedded hostname; treating a
   *  prefix as a hostname and redacting bare occurrences of it corrupts unrelated
   *  text that happens to share that substring (measured: an id `test:opaque-id`
   *  turning a legitimate `https://app.test` into `https://app.<redacted>`). */
  sessionIds?: (string | null | undefined)[];
  /** Bare hostnames to redact wherever they appear as plain text (a loopback host
   *  here is simply a no-op). Only from a KNOWN URL, parsed with `new URL(...)`
   *  (hostnameOf) — the hosted app's base/preview URL, never inferred from an
   *  opaque id's shape. Deliberately NOT `redactPreviewText`'s blanket "any
   *  non-loopback URL" pass — this walks data that legitimately carries OTHER URLs
   *  (a spec's own, already-restored startUrl), so only the LISTED hosts are
   *  touched; see `scrubRules`. */
  hosts?: (string | null | undefined)[];
  /**
   * Values RESOLVED at run time that must never be written down — a `valueFrom`
   * step's credential, read from the environment and typed into the page.
   *
   * The identity class is the same one this pass already handles: a string the run
   * knows verbatim, which every writer downstream must not publish. It differs only
   * in where it came from, so it is scrubbed the same way and replaced by the
   * `reference` that names its source, which is what a reviewer needs to see (an
   * unexplained `<redacted>` is a redaction people turn off).
   *
   * A blank value is dropped rather than scrubbed: splitting on an empty needle
   * would corrupt every string in the tree.
   */
  secrets?: ResolvedSecret[];
}

/**
 * One value a run resolved, and the reference that names where it came from.
 *
 * It travels ON the replay result (`ReplayResult.resolvedSecrets`) rather than dying
 * inside the runner, because the runner is not the last writer: every caller then
 * fetches the replay stream and assembles evidence, and a list that ended at the
 * runner's return left the JSON, the HTML page, the bundle, the PR body and the
 * Playwright attachments protected by session-id redaction alone.
 */
export interface ResolvedSecret {
  value: string;
  /** What replaces it — the reference, so a reviewer can account for the redaction. */
  reference: string;
}

/**
 * Removes every KNOWN backend identity string from EVERY string field of `value` — a
 * whole-object walk, not a per-field allowlist, so a backend session id or hostname
 * buried in a step's error text, a replay event, or a healer's own reasoning is
 * scrubbed the same as a field a targeted redaction would already catch. Must run
 * BEFORE any writer (evidence page, bundle, PR body, Playwright fixture attachments).
 * Removes only the EXACT known strings (`identity`) — it never infers structure from
 * an opaque id.
 */
export function scrubBackendIdentity<T>(
  value: T,
  identity: BackendIdentity,
): T {
  const rules = scrubRules(identity);
  // The guard alone is not work worth doing: with nothing to remove, the text is
  // returned untouched rather than walked.
  if (rules.length <= 1) return value;
  // ONE alternation, longest needle first, applied in a SINGLE pass. Sequential
  // replacements rewrite what an earlier one inserted — scrub A, then scrub a B that
  // occurs inside A's own marker, and the marker comes apart — and a longest-first
  // alternation is also what makes two overlapping secrets resolve to the longer one
  // rather than to whichever happened to be listed first.
  const pattern = new RegExp(rules.map((rule) => rule.pattern).join("|"), "g");
  return walkStrings(value, (text) =>
    text.replace(pattern, (match) => {
      const rule = rules.find((candidate) =>
        new RegExp(`^(?:${candidate.pattern})$`).test(match),
      );
      return rule?.replacement ?? match;
    }),
  ) as T;
}

/**
 * Below this length a secret is NOT removed from free text.
 *
 * A resolved value is an arbitrary string somebody chose for an environment variable,
 * and a short one is a substring of ordinary evidence: measured on the first cut, the
 * secret `1234` rewrote a port, a step id and an order number, and the secret `s`
 * rewrote the outcome `passed` and the action `press`. A corrupted step id breaks the
 * healer's own lookup and a corrupted outcome changes what the CLI branches on, so a
 * redaction that damages the record it protects is strictly worse than the exposure —
 * and no token boundary makes `s` safe.
 *
 * This is a rule about FREE TEXT only. Wherever the position of a value is known — the
 * step log's `inputs`, which records the reference and never the resolution — a short
 * secret is withheld structurally and never depends on this at all. The replay CLI says
 * out loud, once, that a short secret cannot be redacted from free text.
 */
export const SECRET_MIN_FREE_TEXT_LENGTH = 6;

/**
 * Fields whose entire content IS a typed value.
 *
 * The free-text floor exists because a short value is a substring of ordinary evidence
 * — `1234` is also a port, a step id and an order number. That reasoning does not apply
 * where the position of a value is KNOWN: a step's `value`, an audit record's
 * `inputs.value`, a proposed step's `value` hold nothing but the value, so there is no
 * surrounding text to corrupt and no reason to let a four-digit PIN through. The WHOLE
 * field is replaced, never a substring of it.
 */
const KNOWN_VALUE_FIELDS: ReadonlySet<string> = new Set(["value"]);

/**
 * Withholds a resolved value from every known-position field of `value`, at any length.
 *
 * Runs alongside `scrubBackendIdentity`, not instead of it: that pass is about text
 * this code did not shape, and it stops at the free-text floor; this one is about
 * fields this format defines, where the floor has no reason to exist. A field is
 * replaced when it CONTAINS a resolved value — a field holding a secret and a little
 * more is still a secret, and mangling a proposal that fails verification anyway costs
 * nothing next to publishing a credential.
 */
export function withholdKnownValues<T>(
  value: T,
  secrets: ResolvedSecret[] | undefined,
): T {
  const known = (secrets ?? []).filter((secret) => !!secret?.value);
  if (known.length === 0) return value;
  const walk = (node: unknown, key: string | null): unknown => {
    if (typeof node === "string") {
      if (key === null || !KNOWN_VALUE_FIELDS.has(key)) return node;
      const found = known.find((secret) => node.includes(secret.value));
      return found ? found.reference : node;
    }
    if (Array.isArray(node)) return node.map((item) => walk(item, key));
    if (node !== null && typeof node === "object") {
      return Object.fromEntries(
        Object.entries(node as Record<string, unknown>).map(([name, item]) => [
          name,
          walk(item, name),
        ]),
      );
    }
    return node;
  };
  return walk(value, null) as T;
}

/**
 * The same scrub, for an error on its way out of a function.
 *
 * An exception is an artifact: it reaches a terminal, a log and a CI annotation, and it
 * carries two strings — the message and the stack, whose frames quote source text. A
 * backend that fails while parsing a model's reply raises a `SyntaxError` naming what it
 * choked on, which is the page the healer was reading.
 *
 * A NEW error, because `message` and `stack` are not enumerable and the whole-object
 * walk cannot see them. The name is preserved so a caller routing on the error's class
 * still can, and a non-Error throw is returned untouched — there is nothing to rebuild.
 */
export function scrubError(
  thrown: unknown,
  identity: BackendIdentity,
): unknown {
  if (!(thrown instanceof Error)) return thrown;
  const scrubbed = new Error(scrubBackendIdentity(thrown.message, identity));
  scrubbed.name = thrown.name;
  if (thrown.stack !== undefined) {
    scrubbed.stack = scrubBackendIdentity(thrown.stack, identity);
  }
  return scrubbed;
}

/** Whether this resolved value is too short to substitute out of free text safely. */
export function tooShortToScrub(value: string): boolean {
  return value.length < SECRET_MIN_FREE_TEXT_LENGTH;
}

interface ScrubRule {
  /** Regular-expression source matching exactly what this rule removes. */
  pattern: string;
  /** What replaces a match, or `undefined` to leave the match exactly as it is — the
   *  guard rule below, which claims a marker so nothing else can match inside it. */
  replacement?: string;
}

/**
 * Everything this pass has ALREADY produced: the two redaction markers and a session
 * reference. Matched first, and left exactly as found.
 *
 * One pass cannot rewrite what it inserts, but the artifact path scrubs more than once
 * by design — the runner cleans what it returns, and evidence assembly cleans the whole
 * record again with the replay stream folded in. Without this, a resolved value equal to
 * a marker's own contents took the marker apart on the second pass
 * (`<redacted:env.ALPHA_ONE>` → `<<redacted:env.SNEAKY>>`), leaving a reviewer reading a
 * marker that no longer names anything. Claiming the whole marker at its opening
 * character is what makes the pass idempotent: the scan never re-enters one.
 */
const SCRUB_MARKER: ScrubRule = {
  pattern: "<redacted(?::[^<>]*)?>|session_[0-9a-f]{32}",
};

const REGEXP_METACHARACTERS = /[.*+?^${}()|[\]\\]/g;

function escapeForPattern(text: string): string {
  return text.replace(REGEXP_METACHARACTERS, "\\$&");
}

/** Every known string this pass removes, longest needle first. */
function scrubRules(identity: BackendIdentity): ScrubRule[] {
  const rules: ScrubRule[] = [];
  for (const rawId of identity.sessionIds ?? []) {
    if (!rawId) continue;
    rules.push({
      pattern: escapeForPattern(rawId),
      replacement: sessionReference(rawId) ?? "<redacted>",
    });
  }
  for (const secret of identity.secrets ?? []) {
    if (!secret?.value || tooShortToScrub(secret.value)) continue;
    rules.push({
      pattern: escapeForPattern(secret.value),
      replacement: secret.reference,
    });
  }
  for (const host of identity.hosts ?? []) {
    if (!host || isLoopbackHost(host)) continue;
    // The `:port` form goes with the host, exactly as `redactKnownHosts` had it.
    rules.push({
      pattern: `${escapeForPattern(host)}(?::\\d+)?`,
      replacement: "<redacted>",
    });
  }
  // Longest first: an alternation matches the first branch that matches at a position,
  // so without this a secret that is a prefix of another would win over it. The marker
  // guard goes ahead of all of them — it is not competing on length, it is claiming
  // ground no other rule may enter.
  rules.sort((a, b) => b.pattern.length - a.pattern.length);
  return [SCRUB_MARKER, ...rules];
}

/** Loopback hosts (`local.mts`'s server always binds `127.0.0.1`) never carry an
 *  identifying or secret hostname — only a publicly routable host (a Solari sandbox's
 *  `<sandbox-id>-<port>.preview.getsolari.com`) needs its host redacted. The whole
 *  127.0.0.0/8 range is loopback, as is the v4-mapped form — written `::ffff:127.x`,
 *  which Node's URL normalizes to hex (`::ffff:7f00:1`), so both spellings match. An
 *  IPv6 literal's bracket form (`new URL("http://[::1]:8080/").hostname` is `"[::1]"`,
 *  brackets included — WHATWG URL, not this file's choice) is unwrapped before the
 *  same checks run, so a bracketed loopback is recognized the same as a bare one.
 *  A `.localhost` SUBDOMAIN counts too: RFC 6761 reserves the whole `localhost.`
 *  zone to loopback, so `app.localhost` names a machine-local server exactly the way
 *  `127.0.0.1` does and carries no more information than it. This matters because a
 *  spec's own `startUrl` host is fed to the scrub pass (`heal/cli.mts`'s
 *  `previewHostsOf`) — without this, a friendlier local origin would be replaced by
 *  `<redacted>` in the step log and the PR body, which is the opposite of readable.
 *  `fleet.mts`'s `isPrivateHost` already treats `*.localhost` the same way. */
export function isLoopbackHost(hostname: string): boolean {
  const unbracketed =
    hostname.startsWith("[") && hostname.endsWith("]")
      ? hostname.slice(1, -1)
      : hostname;
  const host = unbracketed.toLowerCase();
  return (
    host === "localhost" ||
    host.endsWith(".localhost") ||
    host === "::1" ||
    host.startsWith("127.") ||
    host.startsWith("::ffff:127.") ||
    host.startsWith("::ffff:7f")
  );
}

/**
 * Redact a host's `baseUrl` for the audit record: a Solari sandbox preview URL can
 * carry a live access token in its query or its path (the measured shape — see
 * `host/rebase.mts`'s header — is a query token, but the design must not assume one
 * shape), and its hostname is itself the sandbox id — identifying on its own, and
 * banned from ever appearing on screen. Every search-param VALUE is blanked (the key
 * stays, so a reviewer still sees what kind of param it was), any path segment 16
 * characters or longer — long enough to be an opaque id/token, short enough that no
 * ordinary route segment is this long — is replaced too, and a non-loopback host is
 * replaced outright (the row's `name`/`kind` columns already say which gate served the
 * app, so the host itself carries no auditability the placeholder loses). A loopback
 * host — the Inside gate's own local server — is left alone: it identifies nothing.
 * Malformed input passes through unchanged, mirroring fleet.mts's `new URL()` try/catch.
 */
export function redactPreviewUrl(url: string): string {
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return url;
  }
  const redacted = new URLSearchParams();
  for (const key of parsed.searchParams.keys()) {
    redacted.set(key, "<redacted>");
  }
  parsed.search = redacted.toString();
  parsed.pathname = parsed.pathname
    .split("/")
    .map((segment) => (segment.length >= 16 ? "<redacted>" : segment))
    .join("/");
  if (isLoopbackHost(parsed.hostname)) return parsed.toString();
  const host = parsed.host;
  return parsed.toString().replace(host, "<redacted>");
}

/** A URL embedded in prose, terminated by whitespace or delimiters that never appear
 *  in the URLs this repo prints. Scheme casing varies in model output. */
const URL_IN_TEXT = /https?:\/\/[^\s"'<>)\]]+/gi;
/** Sentence punctuation that follows a URL or token in prose is not part of it. */
const TRAILING_PUNCTUATION = /[.,;:!?]+$/;
/** A bare `pt_token=value` outside (or left over inside) a URL. The value runs to
 *  whitespace, `&`, a quote, or a bracket ONLY — a real token is JWT-shaped (a.b.c),
 *  and stopping at "." would leave `.b.c` behind. Trailing sentence punctuation is
 *  stripped and re-appended, same as the URL pass. */
const BARE_TOKEN = /pt_token=[^\s&"'<>()\[\]]+/g;

export interface RedactPreviewTextOptions {
  /** Preview hostnames to scrub even when they appear WITHOUT a URL scheme (a healer
   *  naming the host in prose). The `:port` form goes with the host. Loopback hosts
   *  are never redacted, listed or not. */
  hosts?: string[];
}

/** Replaces a listed hostname wherever it appears in `text` as plain text, scheme or
 *  not — the `:port` form goes with the host. A loopback host is left alone (it
 *  identifies nothing). Used by `redactPreviewText` (prose); the whole-object walk
 *  states the same rule as one branch of its single alternation (`scrubRules`), because
 *  a per-rule pass is exactly what that pass exists not to do. */
export function redactKnownHosts(
  text: string,
  hosts: (string | null | undefined)[],
): string {
  let out = text;
  for (const host of hosts) {
    if (!host || isLoopbackHost(host)) continue;
    const escaped = host.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    out = out.replace(new RegExp(`${escaped}(:\\d+)?`, "g"), "<redacted>");
  }
  return out;
}

/**
 * The free-text form of `redactPreviewUrl`, for strings that are not URLs but may
 * CARRY one — a healer's reasoning or a failure error printed to the terminal. Every
 * embedded URL goes through `redactPreviewUrl` (token values blanked, non-loopback
 * hosts replaced, loopback left visible), a bare `pt_token=value` that no URL claim
 * covers is scrubbed too, and any listed preview hostname is replaced wherever it
 * appears, scheme or not. One redaction class, two surfaces: the URL helper above
 * extended to stdout, not a second implementation.
 *
 * ONLY for prose meant entirely about the current run's own preview environment
 * (stdout) — it redacts EVERY non-loopback URL it finds, not just the listed hosts,
 * which is wrong for data that legitimately carries OTHER URLs (a spec's own,
 * already-restored startUrl). `scrubBackendIdentity` builds its own host rule
 * instead, for exactly that reason.
 */
export function redactPreviewText(
  text: string,
  options: RedactPreviewTextOptions = {},
): string {
  const urlsRedacted = text.replace(URL_IN_TEXT, (match) => {
    const trailing = TRAILING_PUNCTUATION.exec(match)?.[0] ?? "";
    const url = trailing ? match.slice(0, -trailing.length) : match;
    return redactPreviewUrl(url) + trailing;
  });
  const tokensRedacted = urlsRedacted.replace(BARE_TOKEN, (match) => {
    const trailing = TRAILING_PUNCTUATION.exec(match)?.[0] ?? "";
    return `pt_token=<redacted>${trailing}`;
  });
  return redactKnownHosts(tokensRedacted, options.hosts ?? []);
}

/** Restore the captured origin everywhere in a record — step targets, failure text,
 *  segments — while keeping the audit's app-host row (redacted at the source) intact.
 *  The record is plain data, so a JSON round trip is the whole-object rewrite.
 *  `unrebaseText` only rewrites a FULL matching URL; a bare mention of the hosted
 *  preview's own hostname (no scheme — a Playwright timeout message, say) survives
 *  it untouched, so a second pass over every string catches that too. A no-op
 *  without `appHost` — there is nothing hosted, so nothing to restore or redact. */
export function scrubEvidenceRecord(
  record: EvidenceRecord,
  appHost?: AppHostRebase,
): EvidenceRecord {
  if (!appHost) return record;
  const unrebased = JSON.parse(
    unrebaseText(
      JSON.stringify(record),
      appHost.baseUrl,
      appHost.originalOrigin,
    ),
  ) as EvidenceRecord;
  // The bare-hostname pass runs BEFORE the app-host row is restored below: that row
  // is deliberately exempt (redacted at the source, already the CLI edge's call).
  const scrubbed = scrubBackendIdentity(unrebased, {
    hosts: [hostnameOf(appHost.baseUrl)],
  });
  scrubbed.host = record.host;
  return scrubbed;
}

export function newDecisionId(): string {
  return `dec_${randomBytes(6).toString("hex")}`;
}

const PACKAGE_JSON = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "package.json",
);

export function harnessVersion(): string {
  const { version } = JSON.parse(readFileSync(PACKAGE_JSON, "utf8")) as {
    version: string;
  };
  const sha = process.env.FORMIC_GIT_SHA;
  return sha ? `harness@${version}+${sha}` : `harness@${version}`;
}

/**
 * `events === null` means the driver could not record (DriverSession.fetchReplay's
 * documented capability gap). The record says so explicitly rather than presenting an
 * empty segment list as "nothing happened".
 */
export function assembleEvidence(
  result: ReplayResult,
  events: ReplayEvent[] | null,
  meta: EvidenceMeta,
): EvidenceRecord {
  const segments = events === null ? [] : sliceSegments(events, result.steps);
  for (const segment of segments) {
    if (!isRenderable(segment)) throw new EvidenceIntegrityError(segment);
  }
  const record: EvidenceRecord = {
    decisionId: meta.decisionId ?? newDecisionId(),
    timestamp: meta.timestamp ?? new Date().toISOString(),
    systemVersion: meta.systemVersion ?? harnessVersion(),
    modelVersion: meta.modelVersion ?? null,
    specName: result.specName,
    driver: meta.driver,
    // An empty string is absent, not a value to carry through: scrubBackendIdentity
    // never scrubs "" (splitting on an empty needle would corrupt every string in
    // the tree), so without this the raw "" would otherwise reach every writer
    // untouched — never hashed, never redacted, never even null.
    sessionId: meta.sessionId ? meta.sessionId : null,
    host: meta.host ?? null,
    outcome: result.outcome,
    recording: events === null ? "unavailable" : "captured",
    steps: result.steps,
    segments,
    // Genuinely absent for a backend with no CDP handshake, not merely null: unlike
    // host/sessionId/modelVersion (always-meaningful fields normalised to null), an
    // undefined-valued key here would silently vanish across a JSON round-trip (the
    // "survives JSON" guard below exists to catch exactly that class of field).
    ...(meta.cdpConnect ? { cdpConnect: meta.cdpConnect } : {}),
  };
  // One pass over the WHOLE record — not just the top-level sessionId field — so a
  // bare backend session id, its hostname, or a value this run resolved from the
  // environment, buried in a step's error text or a replay event, is scrubbed the same
  // as the field a targeted redaction would already catch. `record` is built field by
  // field from the result, so the resolved-secret LIST itself is never copied into it.
  // Two rules, because the record holds two kinds of string: free text this code did
  // not shape (an error, a snapshot), and fields that ARE a typed value. The floor
  // applies to the first and has no reason to apply to the second.
  return withholdKnownValues(
    scrubBackendIdentity(record, {
      sessionIds: [meta.sessionId],
      secrets: meta.secrets,
    }),
    meta.secrets,
  );
}

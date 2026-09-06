/**
 * Evidence assembly tests, against the real probe capture — the same fixture the
 * slicer is proven on, so the audit record is exercised on a stream with every quirk
 * measured in probe 2b (Meta/FullSnapshot gaps, mid-window navigations).
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { parseReplay } from "../evidence/segment.mts";
import {
  type ReplayEvent,
  type StepRecord,
  RRWEB_META,
  RRWEB_FULL_SNAPSHOT,
} from "../evidence/types.mts";
import { renderEvidencePage } from "../evidence/page.mts";
import {
  assembleEvidence,
  EvidenceIntegrityError,
  harnessVersion,
  hostnameOf,
  isLoopbackHost,
  redactKnownHosts,
  redactPreviewText,
  redactPreviewUrl,
  scrubBackendIdentity,
  withholdKnownValues,
  sessionReference,
} from "./evidence.mts";
import type { ReplayResult } from "./runner.mts";

const FIXTURES = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "..",
  "fixtures",
);

const events = parseReplay(
  readFileSync(join(FIXTURES, "probe-2b-replay.raw"), "utf8"),
);
const actionLog = JSON.parse(
  readFileSync(join(FIXTURES, "probe-2b-action-log.json"), "utf8"),
) as {
  actions: {
    step: number;
    action: string;
    startedAt: number;
    endedAt: number;
  }[];
};

const steps: StepRecord[] = actionLog.actions.map((a) => ({
  id: `st_probe_${a.step}`,
  index: a.step,
  action: a.action,
  startedAt: a.startedAt,
  endedAt: a.endedAt,
  outcome: "ok" as const,
}));

const passed: ReplayResult = { specName: "probe-2b", outcome: "passed", steps };

test("a recorded run yields one renderable segment per step, keyed by step id", () => {
  const record = assembleEvidence(passed, events, { driver: "solari-browser" });
  assert.equal(record.recording, "captured");
  assert.equal(record.outcome, "passed");
  assert.equal(record.driver, "solari-browser");
  assert.deepEqual(
    record.segments.map((s) => s.stepId),
    steps.map((s) => s.id),
  );
  assert.equal(record.modelVersion, null, "token-free path has no model");
  assert.match(record.systemVersion, /^harness@\d+\.\d+\.\d+/);
  assert.match(record.decisionId, /^dec_[0-9a-f]{12}$/);
});

test("the audit record survives JSON — no field silently becomes null", () => {
  const record = assembleEvidence(passed, events, { driver: "solari-browser" });
  const roundTripped = JSON.parse(JSON.stringify(record)) as typeof record;
  for (const segment of roundTripped.segments) {
    assert.equal(typeof segment.toTimestamp, "number");
    assert.equal(typeof segment.fromTimestamp, "number");
  }
  assert.deepEqual(roundTripped, record);
});

test("a driver that cannot record is stated, not disguised as an empty run", () => {
  const record = assembleEvidence(passed, null, { driver: "local-playwright" });
  assert.equal(record.recording, "unavailable");
  assert.deepEqual(record.segments, []);
  assert.equal(
    record.steps.length,
    steps.length,
    "the step log is still evidence",
  );
});

test("GUARD — a non-renderable segment is refused, never persisted", () => {
  // Strip every Meta event: the DOM snapshots survive, but no segment can establish a
  // viewport, so every one of them would look like evidence and render nothing.
  const withoutMeta = events.filter((e) => e.type !== RRWEB_META);
  assert.throws(
    () => assembleEvidence(passed, withoutMeta, { driver: "solari-browser" }),
    (err: unknown) =>
      err instanceof EvidenceIntegrityError && err.segment.stepIndex === 1,
  );
});

test("the backing session gets a stable non-identifying reference", () => {
  const backingSession =
    "backend.example.test:c46850e2-948a-474b-8911-5ac90a64d69c";
  const first = assembleEvidence(passed, events, {
    driver: "solari-browser",
    sessionId: backingSession,
  });
  const second = assembleEvidence(passed, events, {
    driver: "solari-browser",
    sessionId: backingSession,
  });
  assert.match(first.sessionId ?? "", /^session_[0-9a-f]{32}$/);
  assert.equal(first.sessionId, second.sessionId, "the reference is stable");
  assert.ok(!JSON.stringify(first).includes(backingSession));
});

test("sessionReference: a raw id that already LOOKS like a reference is still hashed, never passed through — a coincidental collision must not publish it verbatim", () => {
  const alreadyReferenceShaped = "session_" + "a".repeat(32);
  const reference = sessionReference(alreadyReferenceShaped);
  assert.notEqual(
    reference,
    alreadyReferenceShaped,
    "the input is hashed like any other raw id, not recognised and returned as-is",
  );
  assert.match(reference ?? "", /^session_[0-9a-f]{32}$/);
  // Still deterministic: the SAME reference-shaped raw id always hashes the same way.
  assert.equal(reference, sessionReference(alreadyReferenceShaped));
});

test("sessionReference: 32 hex characters (128 bits), not 16 — this is the one identifier the repo cannot recompute from the record, so its collision resistance is not cut for display width", () => {
  const digest = sessionReference("any-raw-id");
  assert.equal(digest?.length, "session_".length + 32);
});

test("sessionReference: an empty string is absent, not a value to hash — never emitted as a reference", () => {
  assert.equal(sessionReference(""), null);
});

test("a raw session id embedded verbatim in a step's error text is scrubbed too, not only the top-level sessionId", () => {
  const backingSession =
    "backend.example.test:c46850e2-948a-474b-8911-5ac90a64d69c";
  const failed: ReplayResult = {
    specName: "probe-2b",
    outcome: "failed",
    steps: steps.map((s, i) =>
      i === 0
        ? {
            ...s,
            outcome: "failed",
            error: `connection reset: ${backingSession}`,
          }
        : s,
    ),
  };
  const record = assembleEvidence(failed, events, {
    driver: "solari-browser",
    sessionId: backingSession,
  });
  const serialized = JSON.stringify(record);
  assert.ok(
    !serialized.includes(backingSession),
    "the raw id is gone wherever it appears verbatim",
  );
  assert.match(serialized, /session_[0-9a-f]{32}/);
});

test("REGRESSION — an opaque session id is never parsed for structure: it is matched only whole, never split into a 'hostname prefix'", () => {
  const opaqueId = "test:opaque-id";
  // The exact scenario a colon-split heuristic gets wrong: an id whose prefix, up
  // to its first colon, collides with a SUBSTRING of an unrelated, legitimate host.
  const scrubbed = scrubBackendIdentity(
    { url: "https://app.test", raw: `session ${opaqueId} opened` },
    { sessionIds: [opaqueId] },
  );
  assert.equal(
    scrubbed.url,
    "https://app.test",
    "the legitimate host is untouched — the id's shape is never inferred from it",
  );
  assert.ok(
    !scrubbed.raw.includes(opaqueId),
    "the id itself is still scrubbed wherever it appears verbatim",
  );
  assert.match(scrubbed.raw, new RegExp(sessionReference(opaqueId)!));
});

test('REGRESSION — sink 1: an empty session id assembles as sessionId: null, never the raw "" and never a reference minted for nothing', () => {
  const record = assembleEvidence(passed, events, {
    driver: "solari-browser",
    sessionId: "",
  });
  assert.equal(record.sessionId, null);
  assert.ok(!JSON.stringify(record).includes('"sessionId":""'));
});

test("a replay event carrying the raw session id is scrubbed, not just the top-level field", () => {
  const backingSession =
    "backend.example.test:c46850e2-948a-474b-8911-5ac90a64d69c";
  const taintedEvents = events.map((event, i) =>
    i === 0
      ? { ...event, data: { ...event.data, note: `via ${backingSession}` } }
      : event,
  );
  const record = assembleEvidence(passed, taintedEvents, {
    driver: "solari-browser",
    sessionId: backingSession,
  });
  const serialized = JSON.stringify(record.segments);
  assert.ok(
    !serialized.includes(backingSession),
    "the raw id inside a replay event is gone",
  );
  assert.match(
    serialized,
    /session_[0-9a-f]{32}/,
    "replaced with the reference",
  );
});

test("scrubBackendIdentity: walks nested fields, not only the top level", () => {
  const raw = "backend.example.test:token-abc";
  const reference = sessionReference(raw);
  const scrubbed = scrubBackendIdentity(
    {
      top: raw,
      nested: { deeper: [{ error: `seen ${raw} again` }] },
      untouched: 42,
    },
    { sessionIds: [raw] },
  );
  const serialized = JSON.stringify(scrubbed);
  assert.ok(!serialized.includes(raw));
  assert.equal(scrubbed.untouched, 42, "non-string values pass through");
  assert.equal(scrubbed.top, reference, "the top-level field is the reference");
  assert.equal(
    scrubbed.nested.deeper[0].error,
    `seen ${reference} again`,
    "the SAME reference is used at any depth",
  );
});

test("scrubBackendIdentity: a fragment of an opaque session id is left alone — only the full id, verbatim, is a known identity string", () => {
  const raw = "backend.example.test:token-abc";
  const scrubbed = scrubBackendIdentity(
    { error: "dial tcp backend.example.test:9222: connection refused" },
    { sessionIds: [raw] },
  );
  assert.ok(
    scrubbed.error.includes("backend.example.test"),
    "a bare fragment is not the known id — it is not inferred or redacted",
  );
});

test("scrubBackendIdentity: the hosts option redacts a bare hostname with no session id involved", () => {
  const scrubbed = scrubBackendIdentity(
    { reason: "seen at sbx-1-8080.preview.getsolari.com during the attempt" },
    { hosts: ["sbx-1-8080.preview.getsolari.com"] },
  );
  assert.ok(!scrubbed.reason.includes("preview.getsolari.com"));
});

test("scrubBackendIdentity: a no-op with no known identity, returned as-is", () => {
  const value = { a: "b" };
  assert.equal(scrubBackendIdentity(value, {}), value);
});

test("no host in meta -> host: null; a host is carried through verbatim", () => {
  const bare = assembleEvidence(passed, events, { driver: "solari-browser" });
  assert.equal(bare.host, null);
  const hosted = assembleEvidence(passed, events, {
    driver: "solari-browser",
    host: {
      name: "solari-sandbox",
      kind: "Outside",
      baseUrl: "https://sbx.test/",
    },
  });
  assert.deepEqual(hosted.host, {
    name: "solari-sandbox",
    kind: "Outside",
    baseUrl: "https://sbx.test/",
  });
});

test("redactPreviewUrl: blanks every search-param value, keeps the key", () => {
  assert.equal(
    redactPreviewUrl(
      "https://sbx-1-8080.preview.getsolari.com/?pt_token=abc123",
    ),
    "https://<redacted>/?pt_token=%3Credacted%3E",
  );
  assert.equal(
    redactPreviewUrl("https://sbx.test/?a=1&b=2"),
    "https://<redacted>/?a=%3Credacted%3E&b=%3Credacted%3E",
  );
});

test("redactPreviewUrl: replaces an opaque path segment 16 characters or longer", () => {
  assert.equal(
    redactPreviewUrl("https://sbx.test/tok_aBcDeFgHiJkLmNoP/app"),
    "https://<redacted>/%3Credacted%3E/app",
  );
  assert.equal(
    redactPreviewUrl("https://sbx.test/short/app"),
    "https://<redacted>/short/app",
    "a short segment is left alone, but the host is still redacted",
  );
});

test("redactPreviewUrl: malformed input passes through unchanged", () => {
  assert.equal(redactPreviewUrl("not a url"), "not a url");
  assert.equal(redactPreviewUrl(""), "");
});

test("REGRESSION — isLoopbackHost recognizes the IPv6 bracket form, same as the bare form", () => {
  assert.ok(isLoopbackHost("[::1]"), "bracketed ::1 is loopback");
  assert.ok(
    isLoopbackHost("::1"),
    "bare ::1 is still recognized (no regression)",
  );
  assert.ok(
    isLoopbackHost(new URL("http://[::1]:8080/").hostname),
    "exactly what new URL() hands back for an IPv6 literal — brackets included",
  );
  assert.ok(
    !isLoopbackHost("[2001:db8::1]"),
    "a non-loopback bracketed IPv6 address is still NOT loopback",
  );
});

test("isLoopbackHost recognizes a reserved .localhost subdomain (RFC 6761)", () => {
  assert.ok(
    isLoopbackHost("app.localhost"),
    "a .localhost subdomain resolves to loopback by reservation — it names nothing",
  );
  assert.ok(
    isLoopbackHost("evidence.localhost"),
    "any label under .localhost, not one hard-coded name",
  );
  assert.ok(
    isLoopbackHost("APP.LOCALHOST"),
    "hostname casing is not significant",
  );
  assert.ok(isLoopbackHost("localhost"), "bare localhost (no regression)");
  assert.ok(
    !isLoopbackHost("localhost.example.com"),
    "a host that merely CONTAINS localhost is not loopback",
  );
  assert.ok(
    !isLoopbackHost("notlocalhost"),
    "a host that merely ends with the string is not a .localhost subdomain",
  );
});

test("a .localhost app origin survives redactPreviewUrl unchanged", () => {
  assert.equal(
    redactPreviewUrl("http://app.localhost:4173/"),
    "http://app.localhost:4173/",
    "the demo's own app host must stay readable on screen, like 127.0.0.1 does",
  );
});

test("REGRESSION — a loopback IPv6 URL in bracket form survives the scrub unchanged", () => {
  const url = "http://[::1]:8080/path";
  assert.equal(
    redactPreviewUrl(url),
    url,
    "redactPreviewUrl leaves a loopback host (and its port) alone",
  );
  const hosts = [hostnameOf(url)];
  assert.equal(
    redactKnownHosts(`seen at ${url} during the attempt`, hosts),
    `seen at ${url} during the attempt`,
    "redactKnownHosts (scrubBackendIdentity's host pass) leaves it alone too",
  );
});

test("redactPreviewText: scrubs the token value and the preview host inside free text", () => {
  const redacted = redactPreviewText(
    "the preview at https://286e47f0b5635b69e3c5-4173.preview.getsolari.com/?pt_token=LIVE_TOKEN_VALUE stopped serving",
  );
  assert.ok(!redacted.includes("LIVE_TOKEN_VALUE"), "token value is gone");
  assert.ok(!redacted.includes("286e47f0b5635b69e3c5"), "sandbox id is gone");
  assert.ok(
    !redacted.includes("preview.getsolari.com"),
    "preview host is gone",
  );
  assert.ok(redacted.includes("the preview at"), "the prose survives");
});

test("redactPreviewText: a bare pt_token assignment is scrubbed even outside a URL", () => {
  const redacted = redactPreviewText(
    "the pt_token=abc123secret carries an exp claim",
  );
  assert.ok(!redacted.includes("abc123secret"));
  assert.match(redacted, /pt_token=<redacted>/);
});

test("redactPreviewText: a loopback URL stays visible, its query values still blanked", () => {
  const redacted = redactPreviewText(
    "serving at http://127.0.0.1:8080/?pt_token=x next",
  );
  assert.ok(
    redacted.includes("127.0.0.1:8080"),
    "a loopback host identifies nothing and stays",
  );
  assert.ok(!redacted.includes("pt_token=x"), "the token value is blanked");
});

test("redactPreviewText: sentence punctuation after a URL survives the redaction", () => {
  const redacted = redactPreviewText(
    "element not found: #old at https://sbx.example/?pt_token=SECRET.",
  );
  assert.ok(!redacted.includes("SECRET"));
  assert.ok(!redacted.includes("sbx.example"));
  assert.match(redacted, /\.$/, "the trailing full stop is preserved");
});

test("redactPreviewText: a JWT-shaped (dotted) token is scrubbed whole", () => {
  const redacted = redactPreviewText("the pt_token=aaa.bbb.ccc had expired");
  assert.ok(
    !redacted.includes("aaa.bbb.ccc"),
    "the whole dotted token is gone",
  );
  const atSentenceEnd = redactPreviewText(
    "the token was pt_token=aaa.bbb.ccc.",
  );
  assert.ok(!atSentenceEnd.includes("ccc"));
  assert.match(atSentenceEnd, /\.$/, "sentence punctuation is not token body");
});

test("redactPreviewText: an uppercase scheme URL is redacted", () => {
  const redacted = redactPreviewText(
    "see HTTPS://SBX.EXAMPLE/?pt_token=UPPER_SECRET next",
  );
  assert.ok(!redacted.includes("UPPER_SECRET"));
  assert.ok(!/sbx\.example/i.test(redacted), "the host is gone either way");
});

test("redactPreviewText: the hosts option scrubs a bare preview host, with and without port", () => {
  const redacted = redactPreviewText(
    "host sbx-1-8080.preview.getsolari.com:443 and sbx-1-8080.preview.getsolari.com both 404",
    { hosts: ["sbx-1-8080.preview.getsolari.com"] },
  );
  assert.ok(!redacted.includes("preview.getsolari.com"));
  assert.ok(!redacted.includes("sbx-1-8080"));
});

test("redactPreviewText: the hosts option never redacts a loopback host", () => {
  const redacted = redactPreviewText("local run at 127.0.0.1:8080 fine", {
    hosts: ["127.0.0.1"],
  });
  assert.ok(redacted.includes("127.0.0.1:8080"));
});

test("redactPreviewText: a token inside a URL and a bare token in the same line are both scrubbed", () => {
  const redacted = redactPreviewText(
    "GET https://sbx.example/?pt_token=IN_URL failed; the pt_token=IN_BARE.a.b had expired",
    { hosts: ["sbx.example"] },
  );
  assert.ok(!redacted.includes("IN_URL"));
  assert.ok(!redacted.includes("IN_BARE"));
});

test("redactPreviewUrl: blanks the preview host and sandbox id, not just the token", () => {
  const redacted = redactPreviewUrl(
    "https://286e47f0b5635b69e3c5-4173.preview.getsolari.com/?pt_token=LIVE_TOKEN_VALUE",
  );
  assert.ok(
    !redacted.includes("286e47f0b5635b69e3c5"),
    "the sandbox id must not survive redaction",
  );
  assert.ok(
    !redacted.includes("preview.getsolari.com"),
    "the preview host must not survive redaction",
  );
  assert.match(redacted, /pt_token=%3Credacted%3E/, "the token stays redacted");
  assert.equal(redacted, "https://<redacted>/?pt_token=%3Credacted%3E");
});

test("systemVersion carries the git sha when the environment provides one", () => {
  const previous = process.env.FORMIC_GIT_SHA;
  process.env.FORMIC_GIT_SHA = "abc1234";
  try {
    assert.match(harnessVersion(), /\+abc1234$/);
  } finally {
    if (previous === undefined) delete process.env.FORMIC_GIT_SHA;
    else process.env.FORMIC_GIT_SHA = previous;
  }
});

/**
 * Scrub semantics. A resolved secret is an arbitrary string chosen by whoever set the
 * variable, so the scrub cannot assume it is long, distinctive, or absent from the
 * evidence's own control fields. Three rules, three ways of getting this wrong.
 */
test("scrub: a short secret is left alone in free text — substring rewriting corrupts evidence", () => {
  // Measured on the first cut: the secret "1234" rewrote a port, a step id and an order
  // number; the secret "s" rewrote the outcome "passed" and the action "press". A
  // corrupted step id breaks the healer's own lookup and a corrupted outcome changes
  // what the CLI branches on — a redaction that damages the record it protects is worse
  // than the exposure, and there is no token boundary that makes "s" safe.
  const record = {
    outcome: "passed",
    action: "press",
    url: "http://127.0.0.1:1234/orders/12345",
    stepId: "s1234",
  };
  for (const short of ["1234", "s", "abc12"]) {
    assert.deepEqual(
      scrubBackendIdentity(record, {
        secrets: [{ value: short, reference: "<redacted:env.PIN>" }],
      }),
      record,
      `a ${short.length}-character secret must not be substituted into free text`,
    );
  }
});

test("scrub: a secret long enough to be a token is removed everywhere it appears", () => {
  const scrubbed = scrubBackendIdentity(
    {
      error: 'expected "correct-horse-battery" somewhere',
      nested: ["correct-horse-battery"],
    },
    {
      secrets: [
        {
          value: "correct-horse-battery",
          reference: "<redacted:env.PASSPHRASE>",
        },
      ],
    },
  );
  assert.deepEqual(scrubbed, {
    error: 'expected "<redacted:env.PASSPHRASE>" somewhere',
    nested: ["<redacted:env.PASSPHRASE>"],
  });
});

test("scrub: ONE pass — a replacement marker is never itself rewritten by a later rule", () => {
  // Sequential replacements rewrite what an earlier one inserted: scrub A, then scrub B
  // where B appears inside A's marker, and the marker comes apart. One alternation pass
  // over the string cannot do that, because an inserted marker is never re-scanned.
  const scrubbed = scrubBackendIdentity(
    { error: "typed alpha-secret-value then beta-secret-value" },
    {
      secrets: [
        { value: "alpha-secret-value", reference: "<redacted:env.ALPHA_ONE>" },
        // Contains a token that also occurs inside the marker inserted above.
        { value: "redacted:env.ALPHA_ONE", reference: "<redacted:env.SNEAKY>" },
        { value: "beta-secret-value", reference: "<redacted:env.BETA_TWO>" },
      ],
    },
  );
  assert.equal(
    (scrubbed as { error: string }).error,
    "typed <redacted:env.ALPHA_ONE> then <redacted:env.BETA_TWO>",
  );
});

test("scrub: the LONGEST match wins when two secrets start at the SAME position", () => {
  // The alternatives have to overlap FROM THE SAME INDEX or the ordering is untested:
  // a shorter needle that starts later never competes with a longer one that starts
  // earlier, because the scan reaches the earlier position first and consumes it. Here
  // both match at index 0, so whichever branch is tried first wins — which is exactly
  // what the longest-first sort decides, and the shorter one is listed first on purpose.
  const scrubbed = scrubBackendIdentity(
    { error: "secretvalue" },
    {
      secrets: [
        { value: "secret", reference: "<redacted:env.SHORT>" },
        { value: "secretvalue", reference: "<redacted:env.LONG>" },
      ],
    },
  );
  assert.equal((scrubbed as { error: string }).error, "<redacted:env.LONG>");
});

/**
 * ONE resolved-secret list, reaching every writer.
 *
 * The first cut scrubbed inside `replaySpec` and stopped there: the list died with the
 * function, and every caller then fetched the replay stream and assembled evidence with
 * session-id redaction alone. A canary survived the evidence JSON. So the list travels
 * ON the result, and evidence assembly is where it is applied — which is the one point
 * the JSON, the HTML page, the bundle and the PR body are all built from.
 */
const CANARY = "canary-secret-value";

function resultCarrying(secret: string): ReplayResult {
  return {
    specName: "sign-in",
    outcome: "failed",
    steps: [
      {
        id: "st_1",
        index: 1,
        action: "fill",
        target: "#password",
        inputs: { valueFrom: "env.SIGN_IN_PASSWORD" },
        startedAt: 1000,
        endedAt: 1010,
        outcome: "failed",
        error: `Timeout filling "#password" with "${secret}"`,
        ariaSnapshot: `- textbox "Password": ${secret}`,
      },
    ],
    failure: {
      stepId: "st_1",
      index: 1,
      action: "fill",
      target: "#password",
      phase: "action",
      error: `Timeout filling "#password" with "${secret}"`,
    },
    resolvedSecrets: [
      { value: secret, reference: "<redacted:env.SIGN_IN_PASSWORD>" },
    ],
  };
}

test("SINK — the evidence JSON never carries a resolved value, step log or replay stream", () => {
  // A renderable segment (Meta then FullSnapshot, the slicer's invariant) whose
  // incremental event carries what was typed — the second channel out of the page.
  const streamed: ReplayEvent[] = [
    { type: RRWEB_META, timestamp: 1001, data: { href: "http://app.test/" } },
    { type: RRWEB_FULL_SNAPSHOT, timestamp: 1002, data: { node: {} } },
    { type: 3, timestamp: 1005, data: { source: 5, text: CANARY, id: 7 } },
  ];
  const record = assembleEvidence(resultCarrying(CANARY), streamed, {
    driver: "local-playwright",
    sessionId: "sess-1",
    secrets: resultCarrying(CANARY).resolvedSecrets,
  });
  const json = JSON.stringify(record);
  assert.equal(json.includes(CANARY), false, json);
  // And the list itself is never a field of the record it protects.
  assert.equal(json.includes("resolvedSecrets"), false);
  // The reference is what a reviewer sees in its place.
  assert.match(json, /<redacted:env\.SIGN_IN_PASSWORD>/);
});

test("SINK — the evidence HTML page never carries a resolved value", () => {
  const record = assembleEvidence(resultCarrying(CANARY), null, {
    driver: "local-playwright",
    sessionId: "sess-1",
    secrets: resultCarrying(CANARY).resolvedSecrets,
  });
  const html = renderEvidencePage({
    specName: record.specName,
    outcome: record.outcome,
    initial: record,
  });
  assert.equal(html.includes(CANARY), false);
  assert.match(
    html,
    /&lt;redacted:env\.SIGN_IN_PASSWORD&gt;|<redacted:env\.SIGN_IN_PASSWORD>/,
  );
});

test("scrub: a SECOND pass is a no-op — a marker is never scrubbed again", () => {
  // One pass cannot rewrite what it inserts. But the artifact path scrubs more than
  // once by design: the runner cleans what it returns, and evidence assembly cleans the
  // whole record again with the replay stream folded in. So a value that happens to
  // equal a marker's own contents came apart on the second pass —
  // `<redacted:env.ALPHA_ONE>` became `<<redacted:env.SNEAKY>>` — and a reviewer is
  // reading a redaction marker that no longer names anything.
  const identity = {
    secrets: [
      { value: "alpha-secret-value", reference: "<redacted:env.ALPHA_ONE>" },
      { value: "redacted:env.ALPHA_ONE", reference: "<redacted:env.SNEAKY>" },
    ],
  };
  const once = scrubBackendIdentity(
    { error: "typed alpha-secret-value" },
    identity,
  );
  const twice = scrubBackendIdentity(once, identity);
  assert.deepEqual(once, { error: "typed <redacted:env.ALPHA_ONE>" });
  assert.deepEqual(twice, once, "scrubbing twice must equal scrubbing once");
  // And a third pass, because idempotence is the property, not "survives one repeat".
  assert.deepEqual(scrubBackendIdentity(twice, identity), once);
});

test("scrub: a session reference is not re-scrubbed into a reference of itself", () => {
  // The same shape for the identity half: the marker a session id becomes must survive
  // a second pass even when another known string occurs inside it.
  const raw = "backend-session-abcdef";
  const once = scrubBackendIdentity({ id: raw }, { sessionIds: [raw] });
  const twice = scrubBackendIdentity(once, {
    sessionIds: [raw],
    secrets: [
      {
        value: (once as { id: string }).id.slice(1, -1),
        reference: "<redacted:env.INSIDE>",
      },
    ],
  });
  assert.deepEqual(twice, once);
});

test("known position — a value field is withheld WHOLE, however short it is", () => {
  // The free-text floor exists because a short value is a substring of ordinary
  // evidence. That reasoning does not apply where the position of a typed value is
  // KNOWN: a step's `value`, an audit record's `inputs.value`, a proposed step's
  // `value`. Those fields hold nothing but the value, so there is no surrounding text
  // to corrupt and no reason to let a four-digit PIN through. The whole field is
  // replaced, never a substring of it.
  const secrets = [{ value: "1234", reference: "<redacted:env.PIN>" }];
  assert.deepEqual(
    withholdKnownValues(
      {
        steps: [{ inputs: { value: "1234" }, target: "#pin" }],
        proposal: { step: { action: "fill", value: "1234 then more" } },
        // Free text on the same object is untouched: this pass is about named fields.
        error: "the port 1234 is not a secret here",
      },
      secrets,
    ),
    {
      steps: [{ inputs: { value: "<redacted:env.PIN>" }, target: "#pin" }],
      proposal: { step: { action: "fill", value: "<redacted:env.PIN>" } },
      error: "the port 1234 is not a secret here",
    },
  );
});

test("known position — a value field that holds nothing secret is left exactly alone", () => {
  const record = { steps: [{ inputs: { value: "ordinary" } }] };
  assert.deepEqual(
    withholdKnownValues(record, [
      { value: "1234", reference: "<redacted:env.PIN>" },
    ]),
    record,
  );
});

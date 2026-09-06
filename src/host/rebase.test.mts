/**
 * Rebase rules, each pinned against a shape a real host could hand back: a query-token
 * base, a path-token base, and a bare local port with neither. Offline: no network, no
 * sandbox — every base is a fixture URL string.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { rebaseSpec, unrebaseSpec, unrebaseText } from "./rebase.mts";
import type { Spec } from "../spec/types.mts";

function specWith(startUrl: string, steps: Spec["steps"]): Spec {
  return { name: "t", startUrl, steps };
}

function gotoStep(id: string, target: string): Spec["steps"][number] {
  return { id, index: 1, action: "goto", target };
}

const CAPTURED = "http://127.0.0.1:4173";

test("query-token base: goto and startUrl are rebased onto the host origin, token merged in", () => {
  const spec = specWith(`${CAPTURED}/`, [gotoStep("s1", `${CAPTURED}/`)]);
  const rebased = rebaseSpec(
    spec,
    "https://sbx-4173.preview.example.com/?token=SECRET",
  );
  assert.equal(
    rebased.startUrl,
    "https://sbx-4173.preview.example.com/?token=SECRET",
  );
  assert.equal(
    rebased.steps[0].target,
    "https://sbx-4173.preview.example.com/?token=SECRET",
  );
});

test("path-token base: the old origin-only swap helper would drop the token prefix — this one keeps it", () => {
  const spec = specWith(`${CAPTURED}/`, [gotoStep("s1", `${CAPTURED}/`)]);
  const rebased = rebaseSpec(
    spec,
    "https://sbx.preview.example.com/t/tok_abc123",
  );
  assert.equal(
    rebased.startUrl,
    "https://sbx.preview.example.com/t/tok_abc123/",
  );
  // The naive swap (`url.startsWith(captured) ? baseUrl + url.slice(captured.length) : url`)
  // used elsewhere in the repo would have produced the origin with NO prefix at all.
  assert.notEqual(rebased.startUrl, "https://sbx.preview.example.com/");
});

test("no `//` join when the base pathname is bare root", () => {
  const spec = specWith(`${CAPTURED}/`, [gotoStep("s1", `${CAPTURED}/`)]);
  const rebased = rebaseSpec(spec, "http://127.0.0.1:53211");
  assert.equal(rebased.startUrl, "http://127.0.0.1:53211/");
  assert.ok(!rebased.startUrl.includes("//127.0.0.1:53211//"));
});

test("a captured query survives, with the base's own query overlaid on top", () => {
  const spec = specWith(`${CAPTURED}/`, [
    gotoStep("s1", `${CAPTURED}/?page=2`),
  ]);
  const rebased = rebaseSpec(
    spec,
    "https://sbx-4173.preview.example.com/?token=SECRET",
  );
  const url = new URL(rebased.steps[0].target as string);
  assert.equal(url.searchParams.get("page"), "2");
  assert.equal(url.searchParams.get("token"), "SECRET");
});

test("round-trip deep-equals the original spec for a query-token, a path-token, and a bare base", () => {
  const original = specWith(`${CAPTURED}/`, [
    gotoStep("s1", `${CAPTURED}/`),
    gotoStep("s2", `${CAPTURED}/checkout?step=2`),
  ]);
  for (const base of [
    "https://sbx-4173.preview.example.com/?token=SECRET",
    "https://sbx.preview.example.com/t/tok_abc123",
    "http://127.0.0.1:53211",
  ]) {
    const rebased = rebaseSpec(original, base);
    const restored = unrebaseSpec(rebased, base, CAPTURED);
    assert.deepEqual(restored, original, `round-trip failed for base ${base}`);
  }
});

test("mutation-proof: a non-goto edit made after rebase survives unrebase with no token left anywhere", () => {
  const original = specWith(`${CAPTURED}/`, [
    gotoStep("s1", `${CAPTURED}/`),
    { id: "s2", index: 2, action: "click", target: "#submit" },
  ]);
  const rebased = rebaseSpec(
    original,
    "https://sbx-4173.preview.example.com/?token=SECRET",
  );
  const mutated: Spec = {
    ...rebased,
    steps: rebased.steps.map((step) =>
      step.id === "s2" ? { ...step, target: "#new-selector" } : step,
    ),
  };
  const unrebased = unrebaseSpec(
    mutated,
    "https://sbx-4173.preview.example.com/?token=SECRET",
    CAPTURED,
  );
  const serialized = JSON.stringify(unrebased);
  assert.ok(!serialized.includes("SECRET"));
  assert.equal(
    unrebased.steps.find((step) => step.id === "s2")?.target,
    "#new-selector",
  );
});

test("an off-origin goto target passes through both directions unchanged", () => {
  const spec = specWith(`${CAPTURED}/`, [
    gotoStep("s1", `${CAPTURED}/`),
    gotoStep("s2", "https://analytics.example.com/beacon"),
  ]);
  const rebased = rebaseSpec(
    spec,
    "https://sbx-4173.preview.example.com/?token=SECRET",
  );
  assert.equal(rebased.steps[1].target, "https://analytics.example.com/beacon");
  const unrebased = unrebaseSpec(
    rebased,
    "https://sbx-4173.preview.example.com/?token=SECRET",
    CAPTURED,
  );
  assert.equal(
    unrebased.steps[1].target,
    "https://analytics.example.com/beacon",
  );
});

test("a malformed goto target does not throw and passes through unchanged", () => {
  const spec = specWith(`${CAPTURED}/`, [
    gotoStep("s1", `${CAPTURED}/`),
    gotoStep("s2", "not a url"),
  ]);
  assert.doesNotThrow(() =>
    rebaseSpec(spec, "https://sbx.preview.example.com/"),
  );
  const rebased = rebaseSpec(spec, "https://sbx.preview.example.com/");
  assert.equal(rebased.steps[1].target, "not a url");
  assert.doesNotThrow(() =>
    unrebaseSpec(rebased, "https://sbx.preview.example.com/", CAPTURED),
  );
});

test("MEASURED — the real host-app-in-solari.mts previewUrl shape (redacted): no path segment before `?pt_token=`", () => {
  const base =
    "https://f3047429cdb29b8d522d-4173.preview.getsolari.com?pt_token=FAKE_TOKEN";
  const spec = specWith(`${CAPTURED}/`, [gotoStep("s1", `${CAPTURED}/`)]);
  const rebased = rebaseSpec(spec, base);
  assert.equal(
    rebased.startUrl,
    "https://f3047429cdb29b8d522d-4173.preview.getsolari.com/?pt_token=FAKE_TOKEN",
  );
  const restored = unrebaseSpec(rebased, base, CAPTURED);
  assert.deepEqual(restored, spec);
});

test("unrebaseText: every hosted URL inside JSON, HTML and rrweb-style text is restored to the captured origin with the token gone", () => {
  const base =
    "https://f3047429cdb29b8d522d-4173.preview.getsolari.com?pt_token=FAKE_TOKEN";
  const text = [
    '{"href":"https://f3047429cdb29b8d522d-4173.preview.getsolari.com/?pt_token=FAKE_TOKEN"}',
    '{"src":"https://f3047429cdb29b8d522d-4173.preview.getsolari.com/app.css?pt_token=FAKE_TOKEN&v=2"}',
    '<a href="https://f3047429cdb29b8d522d-4173.preview.getsolari.com/orders/SO-4472?pt_token=FAKE_TOKEN">x</a>',
    "plain https://f3047429cdb29b8d522d-4173.preview.getsolari.com/?pt_token=FAKE_TOKEN end",
    '{"other":"https://analytics.example.com/beacon?pt_token=NOT_OURS"}',
  ].join("\n");
  const out = unrebaseText(text, base, CAPTURED);
  assert.ok(!out.includes("FAKE_TOKEN"), "no token survives");
  assert.ok(!out.includes("preview.getsolari.com"), "no preview host survives");
  assert.ok(out.includes(`{"href":"${CAPTURED}/"}`));
  assert.ok(out.includes(`{"src":"${CAPTURED}/app.css?v=2"}`));
  assert.ok(out.includes(`<a href="${CAPTURED}/orders/SO-4472">x</a>`));
  assert.ok(out.includes(`plain ${CAPTURED}/ end`));
  assert.ok(
    out.includes("https://analytics.example.com/beacon?pt_token=NOT_OURS"),
    "an off-origin URL passes through untouched",
  );
});

test("unrebaseText: a path-token base loses its prefix too", () => {
  const base = "https://sbx.preview.example.com/t/tok_abc123";
  const out = unrebaseText(
    '"https://sbx.preview.example.com/t/tok_abc123/checkout?step=2"',
    base,
    CAPTURED,
  );
  assert.equal(out, `"${CAPTURED}/checkout?step=2"`);
});

test("unrebaseText: a URL closed by a bracket or sentence punctuation keeps that delimiter outside the rewrite", () => {
  const base = "https://sbx.preview.example.com?pt_token=S";
  const out = unrebaseText(
    [
      "background:url(https://sbx.preview.example.com/a.png?pt_token=S)",
      "see https://sbx.preview.example.com/?pt_token=S.",
      "[https://sbx.preview.example.com/x?pt_token=S], {https://sbx.preview.example.com/y?pt_token=S}",
    ].join("\n"),
    base,
    CAPTURED,
  );
  assert.equal(
    out,
    [
      `background:url(${CAPTURED}/a.png)`,
      `see ${CAPTURED}/.`,
      `[${CAPTURED}/x], {${CAPTURED}/y}`,
    ].join("\n"),
  );
});

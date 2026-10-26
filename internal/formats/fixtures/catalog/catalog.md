# Catalog conformance fixtures

## valid/

- `two-sources.json` — two `dir` sources in precedence order.
- `empty.json` — `catalog: 1` with an empty `sources` list — the pre-`formic add` state; the
  schema does not mandate a minimum (deviation-for-adjudication: the spec does not say whether
  an empty catalog is meaningful, and rejecting it would be inventing a constraint the spec
  never states).

## invalid/

- `wrong-kind.json` — a source with `kind: git`; today's schema accepts only `dir`.
- `missing-path.json` — a source omitting the required `path`.

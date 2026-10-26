# Mission conformance fixtures

## valid/

- `heal-checkout.json` — the design doc's own worked example: one step, one input, an
  evidence folder.
- `no-inputs.json` — a saved launch of a tool that declares no inputs: `mission`, `name`,
  one step with `tool` only (no `with`), `evidence`. Proves `with` is optional on a step.

## invalid/

- `two-steps.json` — the deferred username-sweep illustration, two `steps`; v1's
  `maxItems: 1` on `steps` rejects it (sequences are deferred, not designed away).
- `zero-steps.json` — an empty `steps` list; `minItems: 1` rejects it.

# Equip conformance fixtures

## valid/

- `env-form.json` — the design doc's own example: `env.NAME`, the only implemented
  reference form, plus a `providers` choice.
- `reserved-forms.json` — the two reserved-but-unimplemented reference forms,
  `keychain:NAME` and `file:<path>#key`; the schema must round-trip them rather than reject
  them as unknown.

## invalid/

- `literal-secret.json` — a secret value that is not any of the three reference forms
  (`sk-abc123`) — a likely-literal secret, which the equip file must never hold (it holds
  references, never values).

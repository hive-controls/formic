# Conformance fixtures

Used by `src/conformance.test.mts`. Every implementer's own validator (Formic's, the launcher's
Go one) should reach the same valid/invalid verdicts against these fixtures.

## valid/

- `minimal.json` — the smallest manifest that satisfies the schema: `toolspec`, `name`,
  `launch.command` only.
- `e2e-doctor.json` — a full manifest exercising every top-level section (`config`, `status`,
  `health`, `pane`), taken from the design doc's own example.
- `interactive-tool.json` — a full manifest with `pane.interactive: true`; the case a launcher
  must route to a real TTY, never a captured pane.
- `env-outputs-exit.json` — a v1.1 manifest exercising the three additive top-level fields:
  `env` (a required secret and an optional one with a `default`), `outputs.evidenceDir` naming
  the `env` entry that carries the evidence directory, and `exit` mapping codes to meanings.

## invalid/

- `missing-required.json` — omits the required `launch` block. Expected ajv error: instancePath
  `""` (root), `must have required property 'launch'`.
- `wrong-type.json` — `launch.args` is a string instead of an array. Expected ajv error:
  instancePath `/launch/args`, `must be array`.
- `unknown-property.json` — a top-level `unknownField` the schema doesn't declare. Expected ajv
  error: instancePath `""` (root), `must NOT have additional properties` (schema is closed:
  `additionalProperties: false` at the top level).
- `bad-env-name.json` — an `env[0].name` that doesn't match `^[A-Z][A-Z0-9_]*$`. Expected ajv
  error: instancePath `/env/0/name`, `must match pattern "^[A-Z][A-Z0-9_]*$"`.

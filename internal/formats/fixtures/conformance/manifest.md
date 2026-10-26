# Conformance fixtures

Used by `src/conformance.test.mts`. Every implementer's own validator (Formic's, the launcher's
Go one) should reach the same valid/invalid verdicts against these fixtures.

## valid/

- `minimal.json` — the smallest manifest that satisfies the schema: `formic`, `name`,
  `launch.command` only.
- `e2e-doctor.json` — a full manifest exercising every top-level section (`config`, `status`,
  `health`, `pane`), taken from the design doc's own example.
- `interactive-tool.json` — a full manifest with `pane.interactive: true`; the case a launcher
  must route to a real TTY, never a captured pane.
- `env-outputs-exit.json` — a v1.1 manifest exercising the three additive top-level fields:
  `env` (a required secret and an optional one with a `default`), `outputs.evidenceDir` naming
  the `env` entry that carries the evidence directory, and `exit` mapping codes to meanings.
- `needs-and-inputs.json` — the three additive fields `$schema`, `needs`, and `inputs` at once:
  `$schema` (an editor reference, previously rejected as an unknown property), `needs` (one
  entry of each of the four kinds — `secret`, `gate`, `runtime`, `network`), and `inputs` (one
  input exercising `label`, `kind`, `required`, and `sensitive`).
- `arguments-and-options.json` — N13's two additive fields: an `env[]` entry with `options`
  (one with a per-option `description`, one without), and `arguments[]` with one argument of
  each kind (`choice` with a `default` among its `options`, `value`, `flag`).
- `actions-docker-desktop.json` — a real consumer's manifest, key for key: three `actions[]`
  sharing one `launch` prefix, `args`, `whenUp`, per-action `exit` maps, `confirm` on the two that
  interrupt running containers, and a `live.probe` of kind `command` with a `timeout`.
- `actions-librechat.json` — a real consumer's manifest: a `service` need naming another tool and
  its `start` action, a `live.probe` of kind `http`, and an action with `ready.within`.
- `actions-llama-swap.json` — a real consumer's manifest: an action with `lifecycle: service`,
  actions that stand alone with their own `command` and `cwd`, and one with `interactive: true`.
- `live-mcp-proxies.json` — a real consumer's manifest with no actions: a `live.probe` of kind
  `http` with `expect: any`.
- `live-probe-values.json` — an `http` probe with an integer `expect` and a `timeout`.
- `live-probe-command.json` — a `command` probe with `idleExit`.
- `service-lifecycle.json` — a top-level `lifecycle: service` with a `tcp` probe, item-level
  `whenUp` and `ready` (millisecond and minute durations), and a `service` need with no action.

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
- `bad-need-shape.json` — a `needs[0]` of `kind: secret` missing its required `name`, matching
  none of the five `need` shapes. Expected ajv error: instancePath `/needs/0`, `must match
exactly one schema in oneOf`.
- `bad-argument-kind.json` — an `arguments[0].kind` of `switch`, outside the `flag | value |
choice` enum. Expected ajv error: instancePath `/arguments/0/kind`, `must be equal to one of
the allowed values`.
- `option-missing-value.json` — an `env[0].options[0]` with no required `value`. Expected ajv
  error: instancePath `/env/0/options/0`, `must have required property 'value'`.
- `bad-action-name.json` — an `actions[0].name` of `Start`, outside `^[a-z][a-z0-9-]*$`. Expected
  error: instancePath `/actions/0/name`, `must match pattern`.
- `action-unknown-property.json` — an `actions[0]` carrying a key the action does not declare.
  Expected error: instancePath `/actions/0`, `must NOT have additional properties`.
- `empty-confirm.json` — an `actions[0].confirm` of the empty string. Expected error:
  instancePath `/actions/0/confirm`, `must NOT have fewer than 1 characters`.
- `probe-two-kinds.json` — a `live.probe` declaring both `http` and `tcp`, matching none of the
  three probe shapes. Expected error: instancePath `/live/probe`, `must match exactly one schema
in oneOf`.
- `probe-bad-duration.json` — a probe `timeout` of `five seconds`, outside the duration pattern.
  Expected error: instancePath `/live/probe/timeout`, `must match pattern`.
- `idle-exit-zero.json` — a `command` probe with `idleExit: 0`, below the minimum of 1. Expected
  error: instancePath `/live/probe/idleExit`, `must be >= 1`.
- `bad-service-need.json` — a `needs[0]` of `kind: service` with no `tool`, matching none of the
  five `need` shapes. Expected error: instancePath `/needs/0`, `must match exactly one schema in
oneOf`.

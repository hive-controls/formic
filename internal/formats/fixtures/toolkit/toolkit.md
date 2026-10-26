# Toolkit conformance fixtures

## valid/

- `osint-suite.json` — the design doc's own toolkit example: `toolkit`, `name`, `title`,
  `conventions.caseFolder`, and shared `needs` (`runtime` and `network`).
- `minimal.json` — the smallest toolkit descriptor that satisfies the schema: `toolkit`, `name`
  only.
- `service-need.json` — a shared `service` need naming a tool and one of its actions.

## invalid/

- `tools-key-removed.json` — carries a `tools` key, removed along with the seeded source it
  existed to serve; `additionalProperties: false` rejects it.
- `missing-required.json` — omits the required `name`.

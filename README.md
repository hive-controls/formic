# @hive-controls/formic

Formic is the **optional** platform behind any tool that speaks the toolspec contract. It
runs no engine of its own — it resolves configuration (which gate, which healer, which
host, any other declared key) and launches the tool that does the real work.

- **the contract** — a `toolspec.yaml` a tool ships beside its own code, the schema
  published as [`@hive-controls/toolspec`](https://www.npmjs.com/package/@hive-controls/toolspec):
  what to launch, the environment keys it reads (`env[]`), where its evidence lands
  (`outputs.evidenceDir`), and what its exit codes mean (`exit`).
- **the configurator** — Fleet (the gate router: Solari, local, a raw CDP endpoint,
  BrowserStack, Sauce Labs), profiles (a committed `formic.profiles.yaml` plus a
  gitignored `.env` — together a Cocoon), and Launchie (the setup wizard that writes one).
- **`formic run <recipe> [args…]`** — finds the recipe's manifest, resolves only the
  environment keys it declares (secrets redacted whenever printed), and runs the
  recipe's own command with its exit code forwarded — so `formic run e2e-doctor replay
<spec>` is the same process `e2e-doctor replay <spec>` would have started, configured.

## Quickstart

```bash
npm i -D @hive-controls/formic e2e-doctor
npx formic setup                                    # Launchie: writes formic.profiles.yaml + .env
npx formic run e2e-doctor replay path/to/spec.yaml   # resolves env, then runs e2e-doctor
```

### Gates

`FORMIC_GATE` names the gate explicitly and always wins; unset, the first ready
default-eligible gate is chosen (Solari when `SOLARI_API_KEY` resolves, else local) and
announced in the first line of output, so a run is never silently local and a typo is
refused rather than falling back.

| Gate                  | `FORMIC_GATE`                                      | Kind    | Status                                                                                                                                                                                                                                                                  |
| --------------------- | -------------------------------------------------- | ------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Solari                | `solari` (default when `SOLARI_API_KEY` resolves)  | Outside | Resolves to the recipe's own `solari` gate.                                                                                                                                                                                                                             |
| Local Chromium        | `local` (default fallback)                         | Inside  | Resolves to the recipe's own `local` gate — no credential needed.                                                                                                                                                                                                       |
| BrowserStack Automate | `browserstack` (explicit only — never the default) | Outside | Resolves to the recipe's generic remote gate (`…_GATE=cdp` + `…_CDP_URL`). The endpoint builder is UNVERIFIED against a live account since the drivers moved into the recipe (2026-09-06); the earlier VERIFIED run exercised a driver this package no longer contains. |
| Sauce Labs            | `saucelabs` (explicit only — never the default)    | Outside | Resolves to the recipe's local gate plus Playwright's own Selenium-Grid variables (its only documented Playwright route — Sauce exposes no CDP bridge). Same caveat: UNVERIFIED against a live account since the drivers moved out (2026-09-06).                        |

BrowserStack and Sauce Labs need `BROWSERSTACK_USERNAME`/`BROWSERSTACK_ACCESS_KEY` or
`SAUCE_USERNAME`/`SAUCE_ACCESS_KEY` respectively (`.env.example` lists them); absent, a
run naming either explicitly refuses cleanly, naming which variable is missing. Neither
is in the default (no-`FORMIC_GATE`) gate search — a third-party trial credential must
never silently steer the Fleet. Formic never connects to either vendor itself: it only
resolves the credential into the recipe's own generic remote-gate configuration.

## The toolspec contract, one screen

A recipe declares what it needs; Formic (or any launcher) reads it — nobody hand-wires
a second copy of the list. `e2e-doctor`'s own manifest, trimmed:

```yaml
env:
  - name: E2E_DOCTOR_GATE
    description: "Which gate backend replays run through: solari or local."
  - name: E2E_DOCTOR_HEALER_API_KEY
    description: "Bearer for the healer's OpenAI-compatible endpoint."
    secret: true
outputs:
  evidenceDir: E2E_DOCTOR_EVIDENCE_DIR
exit:
  "0": "passed (no repair needed) or healed (repair verified by a green replay)"
  "1": "needs-human (an assertion proposal awaits review) or unhealed"
  "2": "the harness could not start, the healer failed, or evidence could not be written"
```

`formic run` sets only the keys a manifest's `env[]` names (a resolved key the manifest
never declared is dropped, not passed through), masks any key marked `secret: true`
wherever it prints resolved configuration, and forwards the tool's own exit code — the
`exit` table above is what a CI job or git hook is actually gating on.

## What Formic is not

Formic is not an engine, and it never opens a browser or calls a healer itself. Every
recipe on this contract runs completely on its own, with no platform installed at all —
`formic run` only resolves configuration you would otherwise set by hand. See
[`e2e-doctor`](https://github.com/hive-controls/e2e-doctor) for the standalone engine
this platform configures, and [`hivedeck`](https://github.com/hive-controls/hivedeck)
for a terminal UI over the same contract.

## Setup without a TTY

`formic setup --non-interactive` takes the whole choice on the command line:
`--healer-kind <api|agent>`, then `--agent <claude|codex|kimi|grok>` or `--preset <name>`
(`anthropic · openrouter · openai · ollama · lmstudio · llamacpp · vllm · custom`),
`--base-url <url>` for a preset served somewhere other than its default (a model server on
your LAN, a custom endpoint), `--model <name>` (api only), `--api-key-env <VAR>` (api only;
the variable must be set, or pass `--api-key-store ci-name` to record the name alone — a
keyless local preset needs neither), `--timeout-ms <ms>`, and `--profile-name <name>`. The
smoke run still happens — it runs the recipe through `formic run`, or its manifest's own
health check, or reports that the recipe is not installed here; if it fails the profile is
not written and the reason is printed. The healer itself is no longer called: a profile
resolves to variables the recipe reads, so a bad key surfaces on the first heal. The
recipe ships a smaller `setup` of its own for the standalone path (`e2e-doctor setup`).

## Automating it (CI)

`formic run <recipe> …` forwards the recipe's own exit code untouched — a hook or CI job
gates on that directly (`e2e-doctor`'s contract: `0` passed or healed, `1` needs a human,
`2` the tool itself failed). An `api` healer profile is the unattended choice for CI: its
key is one repository secret, and a local preset on your own model server works with no
key at all (mind that a hosted runner cannot reach a LAN host). A headless coding agent
(`agent:claude` etc.) runs on your subscription and needs the vendor's own authentication
on the runner; use it for local runs, and for CI only once that is set up.

Nothing here installs a git hook for you. Configure your repository's `pre-push` or
`pre-commit` hook to run `formic run <recipe> replay …` and, on a failure, the recipe's
own heal command, refusing the push on a non-zero exit. Never write into `.git/hooks/`
from a script your teammates will not get.

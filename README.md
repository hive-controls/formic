# @hive-controls/formic

The shared core all use cases ride: **capture → compile → replay → heal → evidence**.

- **capture** — record a trajectory (human demonstration or agent exploration) in a
  Solari cloud browser session, with session recording on.
- **compile** — turn the recorded trajectory into a human-readable, editable, replayable
  spec (deterministic steps + assertions).
- **replay** — run the spec deterministically (cheap, no LLM tokens on the happy path).
- **heal** — on failure, an agent inspects live state, proposes a repaired step, and
  re-verifies. Healing is never silent: it pairs with assertions and produces a diff.
- **evidence** — every replay/heal/action emits an evidence record: video segment,
  step log with the audit field list (timestamp, decision id, system/model version,
  inputs, action taken). Each step also carries lab performance metrics — navigation
  timing, first paint, LCP on the `goto` step; CLS and long tasks on every step —
  rendered as a compact row on the evidence page. Track and trend only: no budget, no
  assertion, no failure caused by a metric value; a value the gate could not measure
  reads as "—", never a silent 0.

Built: capture, compile, replay, evidence assembly, evidence recording on BOTH backends
— Solari server-side, local via injected rrweb — the heal loop, and the repair-PR
generator. Try it against the sample app:

```bash
FORMIC_GATE=solari npm run e2e-doctor -- heal \
  fixtures/specs/approve-an-order.yaml \
  --app fixtures/sample-app
```

Exit code is the contract: 0 passed, 1 the replay failed, 2 the harness could not start,
finish, or clean up. The Fleet picks the gate: Solari when `SOLARI_API_KEY` resolves, else
the local browser, announced in the first line of output; `FORMIC_GATE=local|solari` forces
one. The evidence record is the same shape either way. An Outside gate refuses a spec whose start URL or
any goto target is on a loopback or private host BEFORE it opens a session, naming the
three ways out (`--app <dir>` to host the app for this run, a sandbox previewUrl or tunnel
you set up yourself, or `FORMIC_GATE=local`).

### Gates

| Gate                  | `FORMIC_GATE`                                      | Kind    | Status                                                                                                                                                   |
| --------------------- | -------------------------------------------------- | ------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Solari                | `solari` (default when `SOLARI_API_KEY` resolves)  | Outside | Exercised — the sample run above                                                                                                                         |
| Local Chromium        | `local` (default fallback)                         | Inside  | Exercised — rrweb-recorded, no credential needed                                                                                                         |
| BrowserStack Automate | `browserstack` (explicit only — never the default) | Outside | VERIFIED against BrowserStack Automate on 2026-09-04 (Playwright connect, page-navigation smoke check, 2 rrweb events captured; session released)        |
| Sauce Labs            | `saucelabs` (explicit only — never the default)    | Outside | VERIFIED against Sauce Labs on 2026-09-04 (Playwright connect via Selenium Grid, page-navigation smoke check, 2 rrweb events captured; session released) |

BrowserStack and Sauce Labs need `BROWSERSTACK_USERNAME`/`BROWSERSTACK_ACCESS_KEY` or
`SAUCE_USERNAME`/`SAUCE_ACCESS_KEY` respectively (`.env.example` lists them); absent, a
run naming either explicitly refuses cleanly, naming which variable is missing. Neither
is in the default (no-`FORMIC_GATE`) gate search — a third-party trial credential must
never silently steer the Fleet. Neither has an `--app <dir>` host yet (both are Outside
gates; `FORMIC_HOST` must point somewhere they can reach), so the VERIFIED stamp above
covers a driver-level connect + rrweb-capture smoke check against a public page, not yet
a full spec replay against the fixture sample app. Sauce Labs exposes no CDP bridge for
Playwright — its
connection goes through Playwright's own Selenium Grid support (env vars, not
`chromium.connect()`), unlike BrowserStack's direct
`wss://cdp.browserstack.com/playwright` bridge.

## E2E Doctor — `e2e-doctor`

The product's command line (use case B). From the repo root:

```bash
npm run e2e-doctor -- setup
npm run e2e-doctor -- heal fixtures/specs/approve-an-order.yaml --app fixtures/sample-app
```

`setup` (Launchie) detects a headless coding agent on PATH, or walks you through an API
preset; runs one smoke heal against the choice before it writes anything; and writes a
Cocoon: the gitignored `.env` (the credential) and a committed, secret-free
`formic.profiles.yaml` (everything else). Re-run it any time to add or change a profile.

```bash
npm run e2e-doctor -- replay fixtures/specs/approve-an-order.yaml
npm run e2e-doctor -- heal   fixtures/specs/approve-an-order.yaml --pr --base main
```

Both commands take `--app <dir>`: hosts the app under test for this run — the
default host follows the gate (`solari-sandbox` on Outside, `local` on Inside), or
`FORMIC_HOST` picks one explicitly (`.env.example` lists it). The spec is rebased onto the
host for the run; `heal`'s repaired spec is unrebased before it is written back, so the
file on disk always carries the ORIGINAL captured origin, never the run's host or a live
token. The evidence audit record carries one redacted `app host` row either way.

`heal --pr` replays the spec; on a failure it asks the configured healer for one repair,
verifies it by a full replay, and opens a pull request from a throwaway worktree branched
off `--base`. The PR carries the repaired spec (a step-keyed diff), the healer's reason
verbatim, the failed step's BEFORE frame and the repaired step's AFTER frame inline, and
a self-contained evidence page (`index.html`, rrweb-player and the replay segments inlined,
playable offline) committed beside the spec. Nothing in the PR description is written by a
model. `e2e-doctor setup` asks for the choices below instead of expecting env — see
[Healing](#healing).

`heal --bundle <dir>` writes the same evidence bundle (page, frames, JSON) to a local
directory without opening a PR — what a CI job uploads as an artifact. Neither `--bundle`
nor `--evidence` touches the spec file: pass `--write` to overwrite it with the repaired
spec (or a recorded assertion proposal); `--pr` writes it into the PR's branch for you.
The bundle's `proposals.json` lists interactive components no step touches and the UI
drift since the previous run — proposed steps, never applied.

The bundle is committed beside the spec: a spec kept under `specs/` gets `evidence/<decision id>/`
next to that directory, any other spec gets `evidence/` next to the file, and
`FORMIC_EVIDENCE_DIR=<repo-relative dir>` overrides both. Evidence bundles are generated
artifacts, committed exactly as rendered: add that directory to your formatter's and
linter's ignore files, or a whole-tree pre-commit check will refuse the repair PR's commit
(measured on the first dogfood run of this repo).

## Healing

```bash
node --import tsx src/heal/heal-demo.mts \
  fixtures/specs/approve-an-order.yaml --write --evidence ./heal-evidence
```

Replays the spec; on a failure, asks the healer for ONE proposal, applies it to a copy,
and replays the whole spec again. Only a green replay counts as healed. A healer may
rewrite a step's target or insert a step; it may only _propose_ an assertion change, which
lands on the spec as `proposedAssertChange` for a human to accept. Exit 0 passed/healed,
1 needs-human/unhealed, 2 the harness itself failed.

The healer is chosen by env (`.env.example` lists every variable):

| `FORMIC_HEALER`                                              | What acts                                                                                                                                 |
| ------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `openai-compatible` (default)                                | Any chat-completions endpoint via `FORMIC_HEALER_BASE_URL` + `FORMIC_HEALER_MODEL`: a router, a local model, Anthropic.                   |
| a saved profile (`preset:`)                                  | `anthropic` · `openrouter` · `openai` · `ollama` · `lmstudio` · `llamacpp` · `vllm` · `custom` (any base URL). Local presets need no key. |
| `agent:claude` · `agent:codex` · `agent:kimi` · `agent:grok` | A headless coding agent on your own subscription, run inside a sandboxed heal workspace.                                                  |
| `agent:custom`                                               | Any CLI, via `FORMIC_HEALER_AGENT_CMD="mycli --flag {prompt}"`.                                                                           |

**An executable path containing whitespace must be quoted** — whitespace is the token boundary, and nothing guesses where an unquoted path ends. Double quotes leave backslashes alone (`\"` is their only escape) and single quotes are fully literal, so a Windows path never needs doubling. In a `.env` file, wrap the whole value in single quotes so the double quotes reach the healer intact:

```
FORMIC_HEALER_AGENT_CMD='"C:\Program Files\mycli\mycli.exe" --flag {prompt}'
```

Or by a saved profile in `formic.profiles.yaml` — a named shortcut for the row above,
never a third backend, committed and secret-free (`e2e-doctor setup` writes it for you).
Secret-free means no key VALUE: a `baseUrl` naming an internal host (`http://models.internal:8080/v1`)
is not a secret, but it is an internal name that lands in git history — keep that in mind:

```yaml
default: my-agent
profiles:
  my-agent:
    kind: agent
    agent: claude
  my-api:
    kind: api
    preset: openrouter
    model: some/model
    apiKeyFrom: env.OPENROUTER_API_KEY
```

`heal --healer <name>` names a saved profile or the `FORMIC_HEALER` grammar directly.
Precedence: `--healer` > `FORMIC_HEALER` > the profile file's own `default:` > today's
env default.

The heal workspace is a directory the agent is pointed at: `HEAL.md` (the brief), the same
brief under `CLAUDE.md` and `AGENTS.md`, `context/` (spec, failure, accessibility snapshot), <!-- export-denylist: ok -->
and `PROPOSAL.yaml`, the only file the harness reads back. Workspaces are kept as audit
material. See the [healer verification matrix](healer-verification-matrix.md)
for observed outcomes by CLI version, breakage class, and gate. The adapter table links
to those results; a configured adapter is not itself a verification claim.

| Adapter        | Verification                                                                      |
| -------------- | --------------------------------------------------------------------------------- |
| `agent:claude` | [Observed matrix](healer-verification-matrix.md)                       |
| `agent:codex`  | [Observed matrix](healer-verification-matrix.md)                       |
| `agent:kimi`   | [Observed matrix](healer-verification-matrix.md)                       |
| `agent:grok`   | [Observed matrix](healer-verification-matrix.md)                       |
| `agent:custom` | [Observed matrix; only when configured](healer-verification-matrix.md) |

## Setup without a TTY

`e2e-doctor setup --non-interactive` takes the whole choice on the command line:
`--healer-kind <api|agent>`, then `--agent <claude|codex|kimi|grok>` or `--preset <name>`
(`anthropic · openrouter · openai · ollama · lmstudio · llamacpp · vllm · custom`),
`--base-url <url>` for a preset served somewhere other than its default (a model server on
your LAN, a custom endpoint), `--model <name>` (api only), `--api-key-env <VAR>` (api only;
the variable must be set, or pass `--api-key-store ci-name` to record the name alone — a
keyless local preset needs neither), `--timeout-ms <ms>`, and `--profile-name <name>`. The
smoke heal still runs; if it fails the profile is not written and the reason is printed.

## Automating it (CI and git hooks)

**Exit codes, every command:** `0` passed or healed (nothing left for a human), `1` the
replay failed, needs a human, or could not be healed, `2` the harness itself could not
start, finish, or clean up. `replay`, `heal`, and `heal-demo.mts` all keep this contract,
so a hook or a CI job gates on it directly.

**Healers in CI:** an `api` profile is the unattended choice — its key is one repository
secret, and a local preset on your own model server works with no key at all (mind that a
hosted runner cannot reach a LAN host). The headless coding agents (`agent:claude` etc.)
run on your subscription and need the vendor's own authentication on the runner; use them
for local runs and for CI only once you have that set up.

**Credentials in specs:** a captured `fill` keeps the value that was typed, and every
heal hands the spec to the healer inside its brief. Record flows with a test-only account
whose password you would happily commit (the sample spec's `hunter2` is exactly that), and
never a real one. A secret-reference field for specs is planned, not shipped.

**Git hooks:** nothing here installs a hook for you. Configure your repository's
`pre-push` or `pre-commit` hook to replay the spec and, on a failure, run
`heal --bundle` and refuse the push on exit `1` or `2`. Never write into `.git/hooks/`
from a script your teammates will not get.

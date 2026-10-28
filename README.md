# formic

formic is the program that discovers, configures, launches, and watches any tool speaking
the Formic file formats (a folder of JSON Schemas this same module ships). Discover tools,
configure them once (a manifest's declared `env[]` keys become a form, not free text — see
"Configure and launch" below), launch them in your terminal or an embedded pane, and follow
their runs and artifacts from one Go binary built with
[Bubble Tea](https://github.com/charmbracelet/bubbletea). The deck (hivedeck) is the
interactive screen bare `formic` opens.

Catalogued sources are ordinary YAML manifests that follow the manifest v1 contract.

## Install

### Homebrew

Homebrew refuses to load a formula from a third-party tap until the tap is trusted, so the
one-time `brew trust` step comes before install:

```sh
brew tap hive-controls/tap && brew trust hive-controls/tap && brew install formic
```

### Release binaries

Download an archive from [GitHub Releases](https://github.com/hive-controls/formic/releases)
and verify it against the release's `checksums.txt`: `darwin_amd64`, `darwin_arm64`,
`linux_amd64`, `linux_arm64`, and `windows_amd64` (built, not exercised — see the terminal
matrix below).

### Developer checkout

formic requires Go 1.26.3 or newer to build. From a checkout of
[hive-controls/formic](https://github.com/hive-controls/formic), install the single binary:

```sh
go install ./cmd/formic
```

Add Go's binary directory (`GOBIN`, or `$(go env GOPATH)/bin` when unset) to `PATH`.
To keep the binary in the checkout instead, use `go build -o formic ./cmd/formic`.

Run `formic` from any directory — it starts anywhere; see "The catalog and the admission
rule" below for what makes a tool show up at all.

Type to filter, use the arrow keys to move, and press Enter for details. Bare `r` opens the
run list; press `/` first when a search itself needs to begin with `r`, `p`, or `q`. `?` expands
the help, and `q` quits when no search is active. A `↑` badge means a higher-priority
definition is shadowing another definition with the same name; the detail view lists every
shadowed source.

## Configure and launch

From a detail view, press `c` to configure the tool. hivedeck writes the result to the
repository's committed `.formic/profile.yaml`. It mirrors `e2e-doctor.profiles.yaml`'s
secret-free, environment-by-name configuration model. Choose the launch mode with the arrow keys,
then enter optional comma-separated argv and environment settings. The profile is stable,
hand-editable YAML; comments on settings the form does not change are preserved.

A manifest that declares the manifest format's v1.1 `env[]` replaces the free-text environment step with
one row per declared key instead: its description is shown, `required` keys are marked, and a
`default` is prefilled. A `secret: true` row is masked and never asks for the secret's own
value — only the name of the environment variable to read it from at launch, stored the same
`env.NAME`-reference way as a hand-typed secret setting above.

An `env[]` entry that lists `options` becomes a chooser instead of free text: arrow keys cycle
`(not set)`, then each option (a `required` entry has no `(not set)` and starts on its first
option when nothing is stored or defaulted). A saved value that is no longer among the options
stays in the cycle, marked, so saving without touching the row never drops it. A secret entry
ignores its options.

A manifest that declares `arguments[]` replaces the free-text arguments step the same way: one
row per declared argument, in manifest order, then one "Extra arguments" row for everything
else. A `flag` is an on/off toggle, a `choice` is a chooser over its `options` (`(not set)`
first), and a `value` is a text input; a `default` only prefills a row that has nothing stored.
The result is stored in the same `args` list as before and appended after `launch.args`, so the
launch path and profile format are unchanged. Arguments are set per repository, never asked per
run (use `inputs[]` for that). A manifest with no `arguments[]` keeps the comma-separated field.

```yaml
arguments:
  - flag: --healer
    kind: choice
    options: [{ value: openai-compatible }, { value: agent:claude }]
    description: Overrides the healer environment setting for this repository.
  - flag: --write
    kind: flag
  - flag: --app
    kind: value
```

With the profile above, choosing `agent:claude`, `--write` and an app directory `web` saves
`args: ["--healer", "agent:claude", "--write", "--app", "web"]`. The detail and spec-prompt
argv previews include the saved arguments.

```yaml
default:
  mode: foreground
tools:
  checks:
    mode: tab
    args: ["--verbose"]
    env:
      FORMIC_GATE: local
      API_TOKEN: env.PROJECT_API_TOKEN
```

The profile is committed, so secret-shaped environment settings must use `env.NAME` and are
resolved from the launching process. Literal secret keys such as `apiKey`, `token`, and
`password`, malformed settings, and unknown profile keys are rejected together in one
diagnostic. A hand-authored `apiKeyFrom: env.NAME` is also accepted. A profile tool that no
longer has a discovered manifest is a doctor warning, not an error.

Press Enter in the detail view to launch. Mode resolution is, from highest to lowest:
manifest `pane.interactive` constraint, `tools.<name>.mode`, `default.mode`, then
`foreground`. The manifest's `pane.prefer` is a displayed hint, not an override.

- `foreground` releases hivedeck's alternate screen, gives the child the real terminal and
  inherited standard streams, waits, then restores hivedeck on the completed run row.
- `tab` detects the host terminal (see the matrix below) and starts
  `formic __replay-row <run-row-path>` in a new terminal surface. Unknown terminals, kitty without
  remote control, and an unresolvable Windows Terminal (checked before launch, so PATH gaps
  are caught ahead of time rather than after a failed spawn) report the fallback and run in
  the foreground. If the terminal client itself still fails to spawn (the tool never started),
  its row is recorded as aborted — never as the tool having failed — and the same tool is
  retried in the foreground as its own new run.
- `pane` keeps the tool inside hivedeck, captured on its own pseudo-terminal and drawn beside
  whatever else is running. See "Panes" below.

A manifest that declares `pane.interactive: true` needs a real terminal and can never be
captured. Such a tool resolves to `tab` if the profile asked for a tab and to `foreground`
otherwise; asking the launcher for a pane directly is refused with the reason, and no run is
recorded for a route that never ran.

`{repo}` in a manifest resolves to its catalogued source's own root path (see "The catalog
and the admission rule" below). A `{spec}` token uses `tools.<name>.spec` from the profile
when present, otherwise hivedeck prompts before launch. Profile `args` are appended after
the manifest argv.

## Actions

A manifest may declare `actions[]`: one item, many actions (start, stop, a TUI, a restart).
Each action shares the manifest's `launch` prefix and adds its own `args`, an optional
`command`/`cwd`, its own `arguments[]`, a `confirm` message, an `exit` table, and
`interactive: true` when it needs a real terminal. A tool with no `actions[]` behaves exactly
as before.

```yaml
launch: { command: C:/devprojects/llama-swap/llama-swap.exe }
actions:
  - name: start
    args: [--listen, "0.0.0.0:8090"]
    lifecycle: service
    whenUp: skip
  - name: stop
    command: node
    args: [scripts/stop-llama-swap.mjs]
    cwd: "{repo}"
    confirm: "Stops the router on :8090."
```

- **The deck.** Enter on such a tool opens an action menu instead of launching. A
  non-interactive action runs captured and leaves a result line in the menu, worded from its
  `exit` table; an interactive action takes over the terminal and returns to the menu; a
  `lifecycle: service` action opens in a tab. An action with `confirm` asks `y/N` first.
  `c` configures the highlighted action: its declared `arguments[]` become rows, stored per
  repository in the profile under the tool's `actions` (never asked per run).
- **The CLI.** `formic launch <tool> [<action>] [--yes] [--detach] [--anyway]`. A tool that declares
  actions is refused without one, with the action list; `--yes` answers a `confirm`; a
  `lifecycle: service` action needs `--detach` (it runs in a tab and the command returns).
  The run row records the action, and the run list shows `tool · action`.
- Missions cannot target an action yet, or a tool that declares `lifecycle: service` (a service never exits, so a mission step could not finish); `formic describe` lists a tool's actions.

## Live state

A manifest may declare how to tell whether the thing it launches is already running:

```yaml
live:
  probe:
    http: http://127.0.0.1:8090/health # or: tcp: 127.0.0.1:8090, or: command: [node, check.mjs]
```

A probe is `http` (status 2xx is up; the body is never read and redirects are not followed),
`tcp` (the connection opens), or `command` (exit 0 is up, anything else down). A command
probe may also declare `idleExit: <code>`: that exit code reports `idle` — the service
answers but has nothing to do. `idle` is only ever reported this way; `formic` parses no
response body. Each probe has a 2 s default timeout, capped at 5 s. The result is `up`,
`idle`, `down`, or `unknown` (no probe declared) and is never stored — it is measured when
asked.

- `formic probe [tool...] [--json]` probes the named tools, or every tool that declares a
  probe, and prints each state with its detail. `formic describe` shows the same line.
- The deck probes the tools visible in the list every 5 s and shows the state in a live
  column; the action menu has a `State:` line. Probes run off the screen's thread, so a slow
  endpoint never freezes the deck. The runs list has a live column of its own right after the tool, shown whenever a row on
  screen belongs to a tool that declares a probe: each row shows that tool's live state (a
  dash when its tool declares no probe or has left the catalog), and only the tools of the
  rows on screen are probed. The run's outcome word is never replaced by it, and a narrow
  terminal truncates the artifact at the end of the line rather than dropping the column.
- An action with `whenUp: skip` does nothing when the tool is already up (or idle): it reports
  "already running" and exits 0 without recording a run. The deck probes before launching.
  `formic launch <tool> [<action>] --anyway` spawns regardless of an answering probe. It
  overrides that skip only: an unmet need, a `confirm`, and the `--detach` rule for a service
  still apply. The deck says "already running" with `Press a to launch anyway.`; `a` runs that
  same launch again past the skip, without asking its `confirm` or inputs twice, and its
  service needs are still checked first. Any other key drops the hint and does its normal job.
- A manifest (or an action) may add `live.ready: {within: 2m}`. `formic launch` then keeps
  probing, once a second, after a task exits 0 or after a service's tab is opened, until the
  probe answers (up or idle) or the window closes, and adds `"ready": true|false` to its
  result (text mode appends `· ready` or `· not ready within 2m0s`). The wait never changes
  the run or the exit code: a service that starts but never answers is still `ok`, exit 0.
  A failed run, or a launch skipped as already running, does not wait. Ctrl-C ends the wait.
- The deck opens the same window in memory when it starts such an action: after a task whose
  exit it watched is 0 (captured, foreground or pane), or after a service's tab opened. A task
  sent to a tab is not watched, so it opens none. While the window is open and nothing has
  answered, the tool's state reads `starting` in the tool list, the action menu and the runs
  list, the tool is probed once a second even when its row is off screen, and the window
  closes on the first up or idle answer or at its deadline. The menu's result line then ends
  `· up` or `· not up within 2m0s`. The window never changes a run row, its outcome or its
  exit code, and it ends with the deck.

## Needs

A `needs[]` entry reports one of three states: **met**, **unmet**, or **unchecked** — formic
did not try, as with a network need, so it never blocks a launch. A `service` need names
another tool (and action) that must be up; it is met when that tool's live probe says `up`,
and a tool whose `service` need names a tool or action that does not exist is invalid
(`formic doctor` reports it). Both `formic launch` and the deck refuse a tool whose `service` need
is unmet; the deck probes the needed tools off its UI thread just before launching, so it never
freezes and the refusal reads like "LibreChat cannot start: service docker-desktop is down".
When the needed tool is in the catalog, the refusal ends with an offer to run its action,
for example "Run Docker Desktop · Start? [y/N]": `y` runs it (a confirming action still asks
first), any other key dismisses, and the original launch is never resumed - launch it again once
the service is up. A need that names no action opens that tool's action menu instead.

## Terminal matrix

The live results below were recorded on macOS 26.4 on 2026-09-05. They cover the named
terminal's launch route, not a promise that every platform or pane combination was exercised.

| Terminal               | Launch surface                                        | Live evidence                                                                                              |
| ---------------------- | ----------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| tmux 3.6a              | New window inside tmux; detected first                | Live-exercised: requested working directory and child environment confirmed on macOS.                      |
| Ghostty 1.3.1          | Tab in the existing window; new window if none exists | Live-exercised: tab placement, working directory, quoted argument, and no-window fallback.                 |
| Terminal.app 2.15      | New **window**, not a tab                             | Live-exercised: successive launches created separate windows; working directory confirmed.                 |
| Windows Terminal / WSL | Windows Terminal adapter                              | **Not exercised** on an interactive desktop; WSL detection and end-to-end tab launch remain unverified.    |
| kitty                  | Tab; requires remote control                          | **Not exercised**; unavailable in the recorded live pass. A failed `kitten @ ls` probe selects foreground. |
| WezTerm                | Spawned terminal surface                              | **Not exercised**; unavailable in the recorded live pass.                                                  |
| iTerm2                 | Tab via AppleScript                                   | **Not exercised**; unavailable in the recorded live pass.                                                  |
| GNOME Terminal         | Tab                                                   | **Not exercised**; no GUI Linux session in the recorded live pass; detection remains unverified.           |

The three AppleScript adapters use shell quoting, with executable tests for spaces, quotes,
newlines, and injection-shaped arguments. Those tests do not establish live terminal support.

## Panes

`mode: pane` runs the tool inside hivedeck, and hivedeck draws every open pane side by side in
one fixed split of equal columns, up to four. There is no layout beyond that split.

The child gets its own pseudo-terminal, so it behaves as it would anywhere: a tool that checks
whether it is talking to a terminal sees one, and takes the same branch it takes in your shell.
**The pane shows that output as plain text.** Colour and cursor movement are not reproduced in
this version — a progress bar that rewrites its own line, or output that paints in colour,
arrives in the pane as the plain lines underneath. What the tool prints is all there; how it
would have looked is not.

In the split: `tab` and `shift+tab` move focus, `x` stops the focused pane, `w` closes the
panes whose tools have already finished, `esc` returns to the tool list, and `q` quits. From
the tool list, bare `p` returns to the split; as with `r` and `q`, press `/` first when a
search itself needs to begin with that letter. A pane shows its tool's output; it does not
forward your keystrokes to the child, which is what `pane.interactive` exists to declare.

What a pane guarantees, whatever the tool does:

- **Bounded output.** Each pane keeps a capped number of lines and a capped number of bytes.
  Overflow drops the oldest lines, never the newest, and the pane header names how many went.
- **A flood cannot starve the interface.** Output is read continuously but drawn on
  hivedeck's own schedule, so a tool writing as fast as it can costs one frame per tick rather
  than one per chunk. Every open pane is composed into a single frame.
- **Nothing raw reaches the screen.** Escape sequences are consumed by the decoder rather than
  replayed — which is both why a child cannot repaint the rest of the display or leak a colour
  into the next pane's column, and why the pane is plain text. A byte that cannot be decoded
  costs the line in progress, and the header says how often that happened.
- **Stopping is explicit and two-stage.** `x` sends a terminate signal and then, only if the
  child is still there after a grace period, a kill. Nothing is ever stopped on a timer: a long
  silent run is legal. Quitting stops every pane, because a captured child left writing into a
  terminal nobody reads would block forever.
- **Resizing reaches the child.** Changing the window resizes every pane's pseudo-terminal, so
  each tool re-reads its own width rather than drawing to the size it started with.

Panes are available where hivedeck can open a pseudo-terminal: macOS and Linux. Whether they
work on Windows is unverified, so that build refuses the pane route with a message rather than
claiming a capability nobody has exercised; the foreground and tab routes are unaffected.

## Run history and artifacts

Every launch receives `FORMIC_RUN_ID` and a file-only `FORMIC_STATUS_FILE`. A tool may
append NDJSON status events to that file; hivedeck never parses status from stdout or stderr,
on any route. After exit, the last terminal `ok`, `failed`, or `needs-human` event supplies the
outcome and the last reported artifact is retained. If no terminal event exists, exit 0 becomes
`ok` and any other exit becomes `failed`.

Pane runs are watched live by hivedeck; tab runs by `formic __replay-row <run-row-path>` in
their own tab. The run list shows the latest event and artifact. Foreground runs are read after
exit because the child owns the screen. Completion records the final outcome after the process
exits; a late status line never reopens a finished run. The tab wrapper reads argv, working
directory, and environment from the saved run row, inherits stdio, and records the exit code.

A foreground run's row records the pid of the formic process supervising it. If that process
dies before completing the row — a crash, a force-quit — the row would otherwise read
"running" forever. Starting the deck, opening the run list, and `formic doctor` each recover such a row (the run list's live refresh does not re-run it): marked
finished with no invented exit code, and a note naming the gone pid. Tab runs are not covered
by this recovery (their real supervisor is the `formic __replay-row` wrapper, not the process
that started them) and are left alone.

Press `r` from the tool list to see runs newest first: run id, tool, mode, start time, exit
code, outcome, and artifact. A finished run whose tool declares the manifest format's v1.1 `exit` table
shows that code's meaning beside it; a run that reported no artifact but whose tool declares
`outputs.evidenceDir` shows the resolved evidence directory instead. Enter on such a row opens
a URL or path with the platform handler (`open`, `xdg-open`, or
`rundll32 url.dll,FileProtocolHandler`). Runs are one JSON file per run beneath the platform
state directory:

- macOS: `~/Library/Application Support/formic/runs`
- Linux: `$XDG_STATE_HOME/formic/runs`, else `~/.local/state/formic/runs`
- Windows: `%LOCALAPPDATA%\formic\runs`

History recorded before the rename, under the same paths with `hivedeck` in place of `formic`,
moves there automatically the first time formic starts.

Outcome vocabulary in the run list: `ok` and `failed` (the tool ran and exited 0 or non-zero),
`running` and the live states while a run is going, and `aborted` — the run never finished
under hivedeck (a tab spawn that failed and was retried in the foreground, or a supervising
process that died), so it has no exit code and a note saying why. `aborted` is shown in its own
warn colour, distinct from `ok` and `failed`.

`formic doctor` checks discovery and the profile without opening the browser. It returns
non-zero for an invalid profile, an invalid manifest, or a profile problem. A shadowed
definition (two catalogued sources declaring the same tool name) is reported without
changing the exit code.

## The catalog and the admission rule

Nothing appears in formic by accident. A source — a directory carrying its own
`.formic/toolkit.yaml` — is scanned only once it has been catalogued with
`formic add <path>`, which validates the descriptor at that moment and refuses otherwise,
leaving the catalog untouched. The catalog lives at `<user-config>/formic/catalog.yaml`; an
absent file is an empty catalog, not an error. Sources are scanned in the catalog's order —
that order is precedence — and for each tool name the first definition wins; a later one of
the same name is reported as shadowed.

Within a catalogued source, tools are discovered live from:

1. `<source>/.formic/tools/*.yaml`
2. Shipped `**/*.formic.yaml` manifests beneath the source; discovery skips `node_modules`,
   `dist`, `build`, `vendor`, `testdata`, and dot-directories.

A toolkit's own `needs:` are inherited by every tool it carries; a tool's own `needs` entry
of the same kind overrides one inherited from the toolkit. `{repo}` in a manifest resolves
to its source's own root path, not the directory formic was started from — the binary
itself starts in any directory (`formic` does not require a Git repository above it).

The user config root is `$XDG_CONFIG_HOME` or `~/.config` on Linux, `%APPDATA%` on Windows, and
`~/Library/Application Support` on macOS. macOS also accepts an existing
`~/.config/formic` directory first for cross-platform setups.

The Formic file formats — JSON Schemas under `schema/` in this module — define what a tool
declares: identity and description, launch command/argv/cwd, configuration schema and file
names, status kind/events, optional health check, pane preference/interactivity, and — as of
manifest v1.1 — the environment variables it reads (`env[]`), where its evidence lands
(`outputs.evidenceDir`), and what its exit codes mean (`exit`). It can also declare the launch
flags it accepts (`arguments[]`, each a `flag`, a `value`, or a `choice` among `options`) and
enumerate the values an env entry accepts (`env[].options`). Configuration
file names describe inputs; hivedeck does not read their secret values. A minimal manifest:

```yaml
formic: 1
name: checks
title: Project checks
description: Run the repository test suite.
launch:
  command: npm
  args: ["test"]
  cwd: "{repo}"
status:
  kind: exit-code
```

A tool whose command differs by operating system — a Python virtualenv's `bin/` versus
`Scripts\`, for example — declares a `darwin`, `linux`, or `windows` block inside `launch`
or `health`. The block for the host formic runs on replaces the base keys it sets
(`command`, `args`, `cwd` in `launch`; `check` in `health`); keys it omits keep their base
values, and a host with no block uses the base:

```yaml
launch:
  command: "{repo}/.venv/bin/mytool"
  windows:
    command: "{repo}/.venv/Scripts/mytool.exe"
health:
  check: [".venv/bin/mytool", "doctor"]
  windows:
    check: [".venv/Scripts/mytool.exe", "doctor"]
```

Unknown substitution tokens, unsupported formic versions, and inconsistent argument
declarations (a `choice` with no options, a `default` outside its options, or the same `flag`
declared twice) are reported by `formic doctor`; invalid definitions are never listed — they are reported by `doctor`.

## Non-interactive verbs

Every verb below supports `--json` for scripts and agent sessions; a refusal is a
structured object naming the reason, exit non-zero, and never launches anything.

- `formic add <path>` — validates `<path>/.formic/toolkit.yaml` and, on success,
  appends the path to the catalog (see above) and prints the resulting catalog.
- `formic list` — every admitted tool (name, title, source, what shadows it), plus
  `uncatalogued`: the current directory's own toolkit when it is valid but not yet added.
- `formic describe <tool>` — the manifest, resolved launch mode, its declared `inputs[]`,
  its declared `arguments[]` and each `env[].options` (in text and `--json`), and each `needs[]` entry with `equipped: "unknown"` (a fixed value until the equip file
  lands).
- `formic launch <tool> [<action>]` — runs the tool through the same launch/runs packages the deck
  uses and reports the run id, outcome, exit code, and artifact. Refuses a tool that needs a
  real terminal when `--json` is set and no terminal is attached. Under `--json` the tool's
  own stdout and stderr go to this process's stderr, so stdout carries only the JSON summary.

## v0.1 scope

v0.1 includes local discovery, per-repo profiles, foreground and terminal-surface launching,
the fixed pane split, run monitoring, and artifact opening. It deliberately excludes:

- Arbitrary tiling or layouts beyond the fixed split, and full colour/cursor emulation.
- A daemon, remote monitoring, or multi-repo browsing.
- A hosted registry or manifest-authoring UI.
- tmux or cmux as dependencies: tmux is an optional launch target; neither is required.

hivedeck is Formic's own screen — bare `formic` opens it — not an independent product; the
platform is the `formic` binary, the file formats, and `<user-config>/formic/`.

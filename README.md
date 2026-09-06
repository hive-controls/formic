# hivedeck

hivedeck is the optional terminal operator UI for any tool that speaks the toolspec
contract — the same contract [`@hive-controls/formic`](https://www.npmjs.com/package/@hive-controls/formic)
configures for automation. Discover tools, configure them once (a manifest's declared
`env[]` keys become a form, not free text — see "Configure and launch" below), launch
them in your terminal or an embedded pane, and follow their runs and artifacts from one
Go binary built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

Repository tools are ordinary YAML manifests that follow the toolspec v1 contract. Projects
without manifests still work: hivedeck turns root and workspace `package.json` scripts into
built-in entries automatically. Workspace scripts have names such as `web--build`; names are
lowercased and normalized to letters, digits, and hyphens. Colliding normalized script names
get deterministic suffixes, and titles preserve the original script keys.

## Install

### Homebrew

Homebrew refuses to load a formula from a third-party tap until the tap is trusted, so the
one-time `brew trust` step comes before install:

```sh
brew tap hive-controls/tap && brew trust hive-controls/tap && brew install hivedeck
```

### Release binaries

Download an archive from [GitHub Releases](https://github.com/hive-controls/hivedeck/releases)
and verify it against the release's `checksums.txt`: `darwin_amd64`, `darwin_arm64`,
`linux_amd64`, `linux_arm64`, and `windows_amd64` (built, not exercised — see the terminal
matrix below).

### Developer checkout

hivedeck requires Go 1.26.3 or newer to build. From a checkout of
[hive-controls/hivedeck](https://github.com/hive-controls/hivedeck), install the single binary:

```sh
go install ./cmd/hivedeck
```

Add Go's binary directory (`GOBIN`, or `$(go env GOPATH)/bin` when unset) to `PATH`.
To keep the binary in the checkout instead, use `go build -o hivedeck ./cmd/hivedeck`.
These source-checkout commands use the bundled toolspec module. `go install
github.com/hive-controls/hivedeck/cmd/hivedeck@<version>` is not supported: go.mod carries a
`replace` directive to that bundled module, which only resolves from inside a checkout.

Run `hivedeck` from any directory beneath a Git repository root.

Type to filter, use the arrow keys to move, and press Enter for details. Bare `r` opens the
run list; press `/` first when a search itself needs to begin with `r`, `p`, or `q`. `?` expands
the help, and `q` quits when no search is active. A `↑` badge means a higher-priority
definition is shadowing another definition with the same name; the detail view lists every
shadowed source.

## Configure and launch

From a detail view, press `c` to configure the tool. hivedeck writes the result to the
repository's committed `.hivedeck/profile.yaml`. It mirrors `formic.profiles.yaml`'s
secret-free, environment-by-name configuration model. Choose the launch mode with the arrow keys,
then enter optional comma-separated argv and environment settings. The profile is stable,
hand-editable YAML; comments on settings the form does not change are preserved.

A manifest that declares toolspec v1.1's `env[]` replaces the free-text environment step with
one row per declared key instead: its description is shown, `required` keys are marked, and a
`default` is prefilled. A `secret: true` row is masked and never asks for the secret's own
value — only the name of the environment variable to read it from at launch, stored the same
`env.NAME`-reference way as a hand-typed secret setting above.

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
  `hivedeck run <run-row-path>` in a new terminal surface. Unknown terminals, and kitty
  without remote control, report the fallback and run in the foreground.
- `pane` keeps the tool inside hivedeck, captured on its own pseudo-terminal and drawn beside
  whatever else is running. See "Panes" below.

A manifest that declares `pane.interactive: true` needs a real terminal and can never be
captured. Such a tool resolves to `tab` if the profile asked for a tab and to `foreground`
otherwise; asking the launcher for a pane directly is refused with the reason, and no run is
recorded for a route that never ran.

`{repo}` in a manifest resolves to the repository root. A `{spec}` token uses
`tools.<name>.spec` from the profile when present, otherwise hivedeck prompts before launch.
Profile `args` are appended after the manifest argv.

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

Every launch receives `HIVEDECK_RUN_ID` and a file-only `HIVEDECK_STATUS_FILE`. A tool may
append NDJSON status events to that file; hivedeck never parses status from stdout or stderr,
on any route. After exit, the last terminal `ok`, `failed`, or `needs-human` event supplies the
outcome and the last reported artifact is retained. If no terminal event exists, exit 0 becomes
`ok` and any other exit becomes `failed`.

Pane runs are watched live by hivedeck; tab runs by `hivedeck run <run-row-path>` in their own
tab. The run list shows the latest event and artifact. Foreground runs are read after exit
because the child owns the screen. Completion records the final outcome after the process
exits; a late status line never reopens a finished run. The tab wrapper reads argv, working
directory, and environment from the saved run row, inherits stdio, and records the exit code.

Press `r` from the tool list to see runs newest first: run id, tool, mode, start time, exit
code, outcome, and artifact. A finished run whose tool declares toolspec v1.1's `exit` table
shows that code's meaning beside it; a run that reported no artifact but whose tool declares
`outputs.evidenceDir` shows the resolved evidence directory instead. Enter on such a row opens
a URL or path with the platform handler (`open`, `xdg-open`, or
`rundll32 url.dll,FileProtocolHandler`). Runs are one JSON file per run beneath the platform
state directory:

- macOS: `~/Library/Application Support/hivedeck/runs`
- Linux: `$XDG_STATE_HOME/hivedeck/runs`, else `~/.local/state/hivedeck/runs`
- Windows: `%LOCALAPPDATA%\hivedeck\runs`

`hivedeck doctor` checks discovery and the profile without opening the browser. It returns
non-zero for invalid profiles/manifests or a repo-local tool shadowing a user-library tool
with the same name but a different launch command. Other shadows are reported without
changing the exit code.

## Manifest discovery

hivedeck walks upward to the nearest `.git` marker and scans one repository. For each tool name,
the first definition wins in this order:

1. `<repo>/.hivedeck/tools/*.yaml`
2. `<user-config>/hivedeck/tools/*.yaml`
3. Shipped `**/*.toolspec.yaml` manifests, then npm scripts; discovery skips `node_modules`,
   `dist`, `build`, `vendor`, `testdata`, and dot-directories. Shipped manifests win over npm scripts.

The user config root is `$XDG_CONFIG_HOME` or `~/.config` on Linux, `%APPDATA%` on Windows, and
`~/Library/Application Support` on macOS. macOS also accepts an existing
`~/.config/hivedeck` library first for cross-platform setups.

The [toolspec contract](https://github.com/hive-controls/toolspec) (npm
[`@hive-controls/toolspec`](https://www.npmjs.com/package/@hive-controls/toolspec)) and its
JSON Schema define what a tool declares:
identity and description, launch command/argv/cwd, configuration schema and file names,
status kind/events, optional health check, pane preference/interactivity, and — as of
toolspec v1.1 — the environment variables it reads (`env[]`), where its evidence lands
(`outputs.evidenceDir`), and what its exit codes mean (`exit`). Configuration
file names describe inputs; hivedeck does not read their secret values. A minimal manifest:

```yaml
toolspec: 1
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

Unknown substitution tokens and unsupported toolspec versions are reported by
`hivedeck doctor`; invalid definitions remain visible with diagnostics.

## v0.1 scope

v0.1 includes local discovery, per-repo profiles, foreground and terminal-surface launching,
the fixed pane split, run monitoring, and artifact opening. It deliberately excludes:

- Arbitrary tiling or layouts beyond the fixed split, and full colour/cursor emulation.
- A daemon, remote monitoring, or multi-repo browsing.
- A hosted registry or manifest-authoring UI.
- tmux or cmux as dependencies: tmux is an optional launch target; neither is required.

hivedeck and Formic are independent products connected by toolspec and the optional status file.

#!/usr/bin/env node
/**
 * e2e-doctor — the E2E Doctor command line.
 *
 *   e2e-doctor replay <spec.yaml> [evidence.json] [--app <dir>]
 *       Replay a spec, token-free. Exit 0 passed, 1 failed, 2 the harness failed.
 *
 *   e2e-doctor record <name> --url <url> [--out <file>] [--gate <name>] [--yes]
 *       Open a browser, record the flow you click through, and write it as a spec.
 *       Ends on any key press. Every assertion is a PROPOSAL you confirm first;
 *       `--yes` accepts them all. Exit 0 written, 1 nothing to write, 2 the harness
 *       failed.
 *
 *   e2e-doctor export <spec.yaml> --target playwright|puppeteer|cypress [--out <path>]
 *       Compile the spec into a runnable test for another framework. The spec stays
 *       the source of truth; the emitted file is a build artifact, regenerated rather
 *       than hand-edited. Without --out it prints to stdout. A construct the target
 *       cannot express refuses by name — there is no partial export. Exit 0 written,
 *       2 refused.
 *
 *   e2e-doctor heal <spec.yaml> [--write] [--evidence <dir>] [--pr] [--base <branch>] [--app <dir>]
 *       Replay; on failure ask the configured healer for a repair, verify it by a
 *       full replay, and (with --pr) open a pull request carrying the repaired spec
 *       and the evidence bundle. Exit 0 passed/healed, 1 needs-human/unhealed, 2 the
 *       harness failed.
 *
 * Backends by env — see .env.example: FORMIC_GATE (solari | local; the Fleet picks Solari
 * when its key resolves, else local, and announces the gate) and FORMIC_HEALER
 * (openai-compatible | agent:claude | agent:codex | agent:kimi | agent:grok |
 * agent:custom). `e2e-doctor setup` asks instead of expecting env.
 *
 * `heal`'s `--healer <name>` names a saved profile from `formic.profiles.yaml` or the
 * FORMIC_HEALER grammar directly; precedence is flag > FORMIC_HEALER > the profiles
 * file's own `default:` > today's env default — resolved and announced (heal/profiles/
 * resolve.mts) right beside the gate and host lines.
 *
 * `--app <dir>` hosts the app under test for this run — required before an
 * Outside gate can reach a spec captured against localhost. FORMIC_HOST picks the host
 * explicitly (solari-sandbox | local); unset, it follows the gate. The spec is rebased
 * onto the host's own baseUrl for the run and, for heal, unrebased before write-back —
 * a repaired spec on disk always carries the ORIGINAL captured origin. A host that
 * fails to open is closed before the "could not start" refusal; one that is open when
 * the process gets SIGINT/SIGTERM is closed before exit 130 — Ctrl-C must not leak a
 * sandbox.
 */
import "dotenv/config";
import { execFile } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { promisify } from "node:util";
import type { ExitCode } from "../replay/cli.mts";
import { runReplayCli } from "../replay/cli.mts";
import { redactPreviewUrl } from "../replay/evidence.mts";
import { loadSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import { createStatusWriter, type StatusWriter } from "../status/stream.mts";
import { compileSpec, isTargetName, TARGET_NAMES } from "../export/index.mts";
import { writeEvidenceBundle } from "../heal/bundle.mts";
import { evidenceDirFor } from "../heal/evidence-dir.mts";
import { findPreviousRecord } from "../heal/previous-record.mts";
import { runHealCli } from "../heal/cli.mts";
import { openRepairPr, type CommandRunner } from "../heal/pr.mts";
import { loadProfiles } from "../heal/profiles/profiles.mts";
import {
  describeHealerSelection,
  resolveHealer,
} from "../heal/profiles/resolve.mts";
import {
  describeSelection,
  preflightSpec,
  selectGate,
  type GateSelection,
} from "../fleet/fleet.mts";
import {
  describeHostSelection,
  selectHost,
  type HostedApp,
} from "../host/host.mts";
import { rebaseSpec } from "../host/rebase.mts";
import {
  acceptsProposal,
  runRecordCli,
  waitForKeyPress,
  writeSpecFile,
} from "./record-cli.mts";
import type { AssertionProposal } from "../capture/proposals.mts";
import { ask } from "./setup/prompts.mts";
import { authorSpec, DslParseError, extendSpec } from "../author/index.mts";
import { importCodegen } from "../author/import-codegen.mts";
import { saveSpec, SpecValidationError, validateSpec } from "../spec/parse.mts";
import { defaultDetectSeams } from "./setup/detect.mts";
import { runSetup } from "./setup/setup.mts";

const execFileAsync = promisify(execFile);

const USAGE = `usage:
  e2e-doctor record <name> --url <url> [--out <file>] [--gate <name>] [--yes] [--include-secrets]
  e2e-doctor replay <spec.yaml> [evidence.json] [--app <dir>]
  e2e-doctor export <spec.yaml> --target playwright|puppeteer|cypress [--out <path>]
  e2e-doctor heal <spec.yaml> [--write] [--evidence <dir>] [--bundle <dir>] [--pr] [--base <branch>] [--app <dir>] [--healer <name>]
  e2e-doctor author <input.yaml> [--out <file>]
  e2e-doctor author --extend <spec.yaml> <input.yaml> [--out <file>]
  e2e-doctor import codegen <file> [--out <file>]
  e2e-doctor setup [--non-interactive …]`;

const EXPORT_VALUE_FLAGS = ["--target", "--out"];

// Value-flags each subcommand's positional parser must skip over, so a flag's own
// value is never mistaken for a spec-file/evidence-file positional. `replay` and
// `heal` share one list today (both accept every flag on it, even ones only `heal`
// acts on); `setup` never calls positionals() at all — it parses its own flags
// entirely inside setup.mts, which is what actually keeps a setup flag's value
// (e.g. `--model`) from ever reaching this function.
const REPLAY_HEAL_VALUE_FLAGS = [
  "--evidence",
  "--bundle",
  "--base",
  "--app",
  "--healer",
];

/** `record`'s own list — its single positional is the spec NAME, and `--url`'s value
 *  must never be mistaken for it. */
const RECORD_VALUE_FLAGS = ["--url", "--out", "--gate"];

// Every flag `record` accepts. It used to read its argv by hand while every other
// subcommand went through the strict checker: `--out` with no value silently defaulted,
// `--out --yes` wrote a file called "--yes", and an unknown flag was ignored — a typo
// that still exits 0, having done something other than what was asked.
const RECORD_FLAGS = ["--url", "--out", "--gate", "--yes", "--include-secrets"];

// `author`/`import`'s own value-flags, same reason as above: `--out`'s and `--extend`'s
// values must never be mistaken for the input/spec positional.
const AUTHOR_VALUE_FLAGS = ["--out", "--extend"];
const IMPORT_VALUE_FLAGS = ["--out"];

// Every flag `author`/`import` accept. Anything else is a typo, and a typo that is
// silently ignored is the worst outcome: `--ou specs/x.yaml` printed the spec to stdout
// and exited 0, so the file the user asked for simply never appeared.
const AUTHOR_FLAGS = ["--out", "--extend"];
const IMPORT_FLAGS = ["--out"];

function flag(argv: string[], name: string): boolean {
  return argv.includes(name);
}
function option(argv: string[], name: string): string | undefined {
  const i = argv.indexOf(name);
  return i >= 0 ? argv[i + 1] : undefined;
}
/**
 * Strict argument check for the subcommands that opted into one: every flag is known, every
 * value-flag actually carries a value (rather than swallowing the next flag), no flag is
 * repeated, and there are no positionals beyond the arity the command declares. Returns the
 * problem to print, or undefined when the argv is clean.
 *
 * `exportCommand` already guards its own `--out` this way; the reason generalises — a flag
 * whose value is missing silently changes what the command DOES (writes a file vs. prints
 * to stdout), so accepting it quietly turns a typo into a wrong outcome that still exits 0.
 */
function checkArgs(
  argv: string[],
  valueFlags: string[],
  knownFlags: string[],
  maxPositionals: number,
): string | undefined {
  const seen = new Set<string>();
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (!arg.startsWith("--")) continue;
    if (!knownFlags.includes(arg)) {
      return `unknown flag "${arg}": expected ${knownFlags.join(" | ")}`;
    }
    if (seen.has(arg)) return `${arg} given more than once`;
    seen.add(arg);
    if (valueFlags.includes(arg)) {
      const value = argv[i + 1];
      if (value === undefined || value.startsWith("--")) {
        return `${arg} requires a value`;
      }
      i++;
    }
  }
  const extra = positionals(argv, valueFlags).slice(maxPositionals);
  if (extra.length > 0) {
    return `unexpected argument(s): ${extra.join(", ")}`;
  }
  return undefined;
}

function positionals(argv: string[], valueFlags: string[]): string[] {
  const out: string[] = [];
  for (let i = 0; i < argv.length; i++) {
    if (argv[i].startsWith("--")) {
      if (valueFlags.includes(argv[i])) i++;
      continue;
    }
    out.push(argv[i]);
  }
  return out;
}

interface AppHost {
  hosted: HostedApp;
  spec: Spec;
  host: { name: string; kind: "Outside" | "Inside"; baseUrl: string };
  originalOrigin: string;
}

/** Opens `--app <dir>` where the selected gate can reach it, and rebases `spec` onto
 *  it. Called from inside the setup try/catch — the caller closes `hosted` on any
 *  later setup failure. */
async function openAppHost(
  appDir: string,
  spec: Spec,
  gate: GateSelection,
): Promise<AppHost> {
  const selection = selectHost(process.env, gate);
  console.log(describeHostSelection(selection));
  const hosted = await selection.host.open(process.env, resolve(appDir));
  const originalOrigin = new URL(spec.startUrl).origin;
  return {
    hosted,
    spec: rebaseSpec(spec, hosted.baseUrl),
    host: {
      name: hosted.name,
      kind: selection.host.kind,
      baseUrl: redactPreviewUrl(hosted.baseUrl),
    },
    originalOrigin,
  };
}

/** Runs on SIGINT/SIGTERM while a host is open: an interrupted run has no exit code
 *  yet and never will, so a launcher tailing the status file is left waiting forever
 *  unless something terminal lands — reported FIRST, synchronously, before the
 *  (async, possibly slow or hanging) host close is even attempted, so a close that
 *  never settles can never also swallow the status report. The host is still closed,
 *  best-effort, before the process exits 130 — an interrupted run must never leak a
 *  live sandbox. */
export async function handleInterrupt(
  hosted: HostedApp,
  status: StatusWriter,
): Promise<void> {
  status.emit({ event: "failed" });
  await hosted.close().catch(() => {});
  process.exit(130);
}

/** While a host is open, SIGINT/SIGTERM must close it before the process exits — an
 *  interrupted run must never leak a live sandbox. Returns a cleanup that removes the
 *  handlers once the run has finished closing the host itself. */
function watchForSignal(
  hosted: HostedApp | undefined,
  status: StatusWriter,
): () => void {
  if (!hosted) return () => {};
  const onSignal = () => {
    void handleInterrupt(hosted, status);
  };
  process.once("SIGINT", onSignal);
  process.once("SIGTERM", onSignal);
  return () => {
    process.removeListener("SIGINT", onSignal);
    process.removeListener("SIGTERM", onSignal);
  };
}

const runCommand: CommandRunner = async (command, args, options) => {
  const { stdout } = await execFileAsync(command, args, {
    cwd: options.cwd,
    maxBuffer: 16 * 1024 * 1024,
  });
  return { stdout };
};

async function repoRootOf(file: string): Promise<string> {
  const { stdout } = await execFileAsync(
    "git",
    ["rev-parse", "--show-toplevel"],
    {
      cwd: resolve(file, ".."),
    },
  );
  return stdout.trim();
}

/**
 * One line per proposal, answered y/n. Adopting an expectation is the human's call and
 * always has been, so ONLY `y` or `yes` adopts one; every other answer — a typo, a
 * stray newline, `maybe`, a paste — declines.
 *
 * It used to test "does the answer start with n", which made `maybe` an approval and
 * an accidental keypress an approval too. A declined proposal announces itself
 * immediately (the spec refuses to write and names the bare step); a proposal adopted
 * by accident never announces itself at all. The prompt advertises the strict default.
 */
async function askAboutProposal(proposal: AssertionProposal): Promise<boolean> {
  const answer = await ask(
    {
      input: process.stdin,
      output: process.stdout,
      isTty: Boolean(process.stdin.isTTY),
    },
    `  assert: ${proposal.summary}\n    accept? [y/N] `,
  );
  return acceptsProposal(answer);
}

async function record(argv: string[]): Promise<ExitCode> {
  const problem = checkArgs(argv, RECORD_VALUE_FLAGS, RECORD_FLAGS, 1);
  if (problem) {
    console.error(problem);
    return 2;
  }
  const [specName] = positionals(argv, RECORD_VALUE_FLAGS);
  const startUrl = option(argv, "--url");
  if (!specName || !startUrl) {
    console.error(USAGE);
    return 2;
  }
  const gateName = option(argv, "--gate");
  if (gateName) process.env.FORMIC_GATE = gateName;
  // A recording is driven by a human's hand; the browser has to be on screen. Set, not
  // defaulted: `FORMIC_HEADED=0` in the environment (an inherited CI export, a shell
  // that replays runs headless) survived `??=` and opened a window nobody could see,
  // so the recording ended with nothing recorded and no reason given.
  process.env.FORMIC_HEADED = "1";
  let gate: GateSelection;
  try {
    gate = selectGate();
    console.log(describeSelection(gate));
  } catch (error) {
    console.error(`could not start: ${(error as Error).message}`);
    return 2;
  }
  console.log(`recording ${specName} from ${startUrl} — press any key to stop`);
  const keyPress = waitForKeyPress(process.stdin);
  return runRecordCli({
    specName,
    startUrl,
    driver: gate.driver,
    outFile: option(argv, "--out") ?? join("specs", `${specName}.yaml`),
    stop: keyPress.pressed,
    cancelStop: keyPress.cancel,
    includeSecrets: flag(argv, "--include-secrets"),
    assumeYes: flag(argv, "--yes"),
    confirm: flag(argv, "--yes") ? async () => true : askAboutProposal,
    io: { log: console.log, error: console.error },
    writeSpec: writeSpecFile,
  });
}

async function replay(argv: string[]): Promise<ExitCode> {
  const [specFile, evidenceFile] = positionals(argv, REPLAY_HEAL_VALUE_FLAGS);
  if (!specFile) {
    console.error(USAGE);
    return 2;
  }
  const status = createStatusWriter(process.env.HIVEDECK_STATUS_FILE);
  status.emit({ event: "started" });
  const appDir = option(argv, "--app");
  let spec;
  let driver;
  let appHost: AppHost | undefined;
  try {
    spec = loadSpec(readFileSync(specFile, "utf8"));
    const gate = selectGate();
    driver = gate.driver;
    console.log(describeSelection(gate));
    if (appDir) {
      appHost = await openAppHost(appDir, spec, gate);
      spec = appHost.spec;
    }
    preflightSpec(spec, gate);
  } catch (error) {
    await appHost?.hosted.close().catch((closeError: unknown) => {
      console.error(`cleanup failed: ${(closeError as Error).message}`);
    });
    console.error(`could not start: ${(error as Error).message}`);
    status.emit({ event: "failed" });
    return 2;
  }
  const stopWatching = watchForSignal(appHost?.hosted, status);
  try {
    return await runReplayCli({
      spec,
      driver,
      evidenceFile,
      io: { log: console.log, error: console.error },
      status,
      writeEvidence: writeFileSync,
      host: appHost?.host,
      appHost: appHost
        ? {
            baseUrl: appHost.hosted.baseUrl,
            originalOrigin: appHost.originalOrigin,
          }
        : undefined,
      // Closing the hosted app is part of the run's own outcome, not afterthought
      // cleanup: it must finish before the terminal status is reported, so an "ok"
      // never precedes a close that then fails.
      cleanup: appHost ? () => appHost.hosted.close() : undefined,
    });
  } finally {
    stopWatching();
  }
}

async function healCommand(argv: string[]): Promise<ExitCode> {
  const [specFile] = positionals(argv, REPLAY_HEAL_VALUE_FLAGS);
  if (!specFile) {
    console.error(USAGE);
    return 2;
  }
  const status = createStatusWriter(process.env.HIVEDECK_STATUS_FILE);
  status.emit({ event: "started" });
  const write = flag(argv, "--write");
  const pr = flag(argv, "--pr");
  const evidenceDir = option(argv, "--evidence");
  const bundleDirFlag = option(argv, "--bundle");
  const base = option(argv, "--base");
  const appDir = option(argv, "--app");
  const healerFlag = option(argv, "--healer");

  let spec;
  let driver;
  let healer;
  let appHost: AppHost | undefined;
  // Captured before any --app rebase: the bundle's internal diff and the PR must
  // compare the healed spec against the spec as it sits on disk, never against the
  // run's rebased copy — otherwise both would show the host origin as a "change".
  let onDiskSpec: Spec | undefined;
  try {
    spec = loadSpec(readFileSync(specFile, "utf8"));
    onDiskSpec = spec;
    const gate = selectGate();
    driver = gate.driver;
    console.log(describeSelection(gate));
    if (appDir) {
      appHost = await openAppHost(appDir, spec, gate);
      spec = appHost.spec;
    }
    preflightSpec(spec, gate);
    const profiles = loadProfiles(process.cwd());
    const selection = resolveHealer({
      flagValue: healerFlag,
      env: process.env,
      profiles,
    });
    healer = selection.healer;
    console.log(describeHealerSelection(selection));
  } catch (error) {
    await appHost?.hosted.close().catch((closeError: unknown) => {
      console.error(`cleanup failed: ${(closeError as Error).message}`);
    });
    console.error(`could not start: ${(error as Error).message}`);
    status.emit({ event: "failed" });
    return 2;
  }

  const stopWatching = watchForSignal(appHost?.hosted, status);
  try {
    return await runHealCli({
      spec,
      driver,
      healer,
      io: { log: console.log, error: console.error },
      status,
      options: {
        maxAttempts: process.env.FORMIC_HEALER_MAX_ATTEMPTS
          ? Number(process.env.FORMIC_HEALER_MAX_ATTEMPTS)
          : undefined,
        host: appHost?.host,
      },
      evidenceDir,
      // Where proposals will actually be readable: a PR exists only when something
      // was repaired, so a passing run with --pr alone has nowhere to point.
      proposalsHint: (result) =>
        bundleDirFlag
          ? `${bundleDirFlag}/proposals.json`
          : evidenceDir
            ? `${evidenceDir}/proposals.json`
            : pr && result.outcome !== "passed"
              ? "the repair PR"
              : undefined,
      writeSpec: write ? (yaml) => writeFileSync(specFile, yaml) : undefined,
      appHost: appHost
        ? {
            baseUrl: appHost.hosted.baseUrl,
            originalOrigin: appHost.originalOrigin,
          }
        : undefined,
      afterHeal: async (result, io, recordArtifact) => {
        let artifact: string | undefined;
        // The previous record of this spec, from the evidence directory repair PRs
        // commit to — what the UI-drift section compares against. Best-effort: outside
        // a git repo, or with no earlier bundle, there is simply no drift section.
        const previousRecord = async () => {
          try {
            const root = await repoRootOf(specFile);
            const rel = relative(root, resolve(specFile));
            const evidenceRoot = join(
              root,
              dirname(
                evidenceDirFor(rel, "x", process.env.FORMIC_EVIDENCE_DIR),
              ),
            );
            return findPreviousRecord(evidenceRoot, result.spec.name, {
              excludeDecisionId: result.initial.evidence.decisionId,
            });
          } catch {
            return undefined;
          }
        };
        // --bundle: the full evidence bundle (page + frames + JSON) on disk, no PR —
        // what a CI job uploads as an artifact, and what a human opens locally.
        if (bundleDirFlag) {
          const local = await writeEvidenceBundle(
            onDiskSpec!,
            result,
            bundleDirFlag,
            { previous: await previousRecord() },
          );
          io.log(
            `  evidence bundle -> ${local.dir} (${local.frames.length} frame(s))`,
          );
          artifact = local.pageFile;
          // Recorded immediately: this bundle is already durable on disk, so a PR
          // step that fails below must not lose it off the terminal status event.
          recordArtifact(artifact);
        }
        if (!pr) return artifact;
        if (result.outcome === "passed") {
          io.log("  no PR: nothing to repair");
          return artifact;
        }
        const repoRoot = await repoRootOf(specFile);
        const decision = result.initial.evidence.decisionId;
        const bundleWorkDir = mkdtempSync(join(tmpdir(), "e2e-doctor-bundle-"));
        const bundle = await writeEvidenceBundle(
          onDiskSpec!,
          result,
          bundleWorkDir,
          { previous: await previousRecord() },
        );
        // Recorded immediately, same reason as the --bundle write above: this bundle
        // is already durable on disk (bundleWorkDir), so an openRepairPr failure
        // below must not lose it off the terminal status event.
        recordArtifact(bundle.pageFile);
        const specRel = relative(repoRoot, resolve(specFile));
        // Beside the spec by default (`specs/a.yaml` -> `evidence/<decision>`), or
        // wherever FORMIC_EVIDENCE_DIR points inside the repo.
        const bundleDir = evidenceDirFor(
          specRel,
          decision,
          process.env.FORMIC_EVIDENCE_DIR,
        );
        const opened = await openRepairPr({
          repoRoot,
          specFile: specRel,
          bundleDir,
          original: onDiskSpec!,
          result,
          bundle,
          base,
          run: runCommand,
        });
        io.log(`  pull request: ${opened.url} (${opened.branch})`);
        return opened.url;
      },
      // Closing the hosted app is part of the run's own outcome, not afterthought
      // cleanup: it must finish before the terminal status is reported, so an "ok"
      // never precedes a close that then fails.
      cleanup: appHost ? () => appHost.hosted.close() : undefined,
    });
  } finally {
    stopWatching();
  }
}

/** `author`: DSL text -> a spec, or DSL text appended to an existing spec (`--extend`).
 *  Deterministic and token-free — no browser, no healer, no network — so a malformed
 *  input or a spec that fails spec/parse.mts's own validation is refused here, not
 *  discovered later by `replay`. */
async function authorCommand(argv: string[]): Promise<ExitCode> {
  const argError = checkArgs(argv, AUTHOR_VALUE_FLAGS, AUTHOR_FLAGS, 1);
  if (argError) {
    console.error(argError);
    console.error(USAGE);
    return 2;
  }
  const [inputFile] = positionals(argv, AUTHOR_VALUE_FLAGS);
  const extendTarget = option(argv, "--extend");
  const outFile = option(argv, "--out");
  if (!inputFile) {
    console.error(USAGE);
    return 2;
  }
  try {
    const additions = readFileSync(inputFile, "utf8");
    const spec = extendTarget
      ? extendSpec(loadSpec(readFileSync(extendTarget, "utf8")), additions)
      : authorSpec(additions);
    const yaml = saveSpec(spec);
    if (outFile) {
      writeFileSync(outFile, yaml);
      console.log(`authored: ${outFile} (${spec.steps.length} step(s))`);
    } else {
      console.log(yaml);
    }
    return 0;
  } catch (error) {
    if (
      error instanceof DslParseError ||
      error instanceof SpecValidationError
    ) {
      console.error(error.message);
      return 2;
    }
    console.error(`could not author: ${(error as Error).message}`);
    return 2;
  }
}

/** `import codegen`: a Playwright codegen (.spec.ts) script -> a spec. Unsupported
 *  constructs are printed, never silently dropped; nothing importable at all is a
 *  refusal (exit 2), same as any other malformed input.
 *
 *  The spec is validated BEFORE anything is written. An import that emitted YAML the
 *  product's own `loadSpec` then refuses is worse than no file: the failure surfaces later,
 *  somewhere else, with no line to go and fix. So a script codegen recorded without an
 *  `expect()` after a state-changing action is reported at its source line and the command
 *  writes nothing — it never invents the missing assertion, and never relaxes the rule. */
async function importCommand(argv: string[]): Promise<ExitCode> {
  const argError = checkArgs(argv, IMPORT_VALUE_FLAGS, IMPORT_FLAGS, 2);
  if (argError) {
    console.error(argError);
    console.error(USAGE);
    return 2;
  }
  const [kind, file] = positionals(argv, IMPORT_VALUE_FLAGS);
  const outFile = option(argv, "--out");
  if (kind !== "codegen" || !file) {
    console.error(USAGE);
    return 2;
  }
  try {
    const { spec, unsupported } = importCodegen(readFileSync(file, "utf8"));
    for (const item of unsupported) console.error(`unsupported: ${item}`);
    if (spec.steps.length === 0) {
      console.error("no steps could be imported");
      return 2;
    }
    try {
      validateSpec(spec);
    } catch (error) {
      console.error((error as Error).message);
      console.error("nothing written — the import is incomplete");
      return 2;
    }
    const yaml = saveSpec(spec);
    if (outFile) {
      writeFileSync(outFile, yaml);
      console.log(`imported: ${outFile} (${spec.steps.length} step(s))`);
    } else {
      console.log(yaml);
    }
    return 0;
  } catch (error) {
    console.error(`could not import: ${(error as Error).message}`);
    return 2;
  }
}

/**
 * Compile a spec into another framework's test. Deliberately offline and browser-free:
 * an export is a source-to-source build, so it must work in a checkout with no gate
 * configured and no browser installed.
 */
function exportCommand(argv: string[]): ExitCode {
  const [specFile] = positionals(argv, EXPORT_VALUE_FLAGS);
  const target = option(argv, "--target");
  if (!specFile || !target) {
    console.error(USAGE);
    return 2;
  }
  if (!isTargetName(target)) {
    console.error(
      `unknown --target "${target}": expected ${TARGET_NAMES.join(" | ")}`,
    );
    return 2;
  }
  let compiled;
  try {
    const spec = loadSpec(readFileSync(specFile, "utf8"));
    compiled = compileSpec(spec, target, { specPath: specFile });
  } catch (error) {
    // A refusal is the product working: it names the construct instead of emitting a
    // test that would pass while meaning something else.
    console.error(`could not export: ${(error as Error).message}`);
    return 2;
  }
  // `--out` with nothing after it, or with the next flag after it, is a typo — writing
  // the export to stdout instead would look like success and silently produce no file.
  const out = option(argv, "--out");
  if (argv.includes("--out") && (out === undefined || out.startsWith("--"))) {
    console.error("--out requires a file path");
    return 2;
  }
  if (!out) {
    process.stdout.write(compiled.source);
    return 0;
  }
  mkdirSync(dirname(resolve(out)), { recursive: true });
  writeFileSync(out, compiled.source);
  console.log(`exported ${specFile} -> ${out} (${target})`);
  return 0;
}

const COMMAND_NAMES = [
  "record",
  "replay",
  "export",
  "heal",
  "author",
  "import",
  "setup",
];

async function main(argv: string[]): Promise<ExitCode> {
  const [command, ...rest] = argv;
  if (command === "--help" || command === "-h") {
    console.log(USAGE);
    return 0;
  }
  if (
    command &&
    COMMAND_NAMES.includes(command) &&
    (rest.includes("--help") || rest.includes("-h"))
  ) {
    console.log(USAGE);
    return 0;
  }
  if (command === "record") return record(rest);
  if (command === "replay") return replay(rest);
  if (command === "export") return exportCommand(rest);
  if (command === "heal") return healCommand(rest);
  if (command === "author") return authorCommand(rest);
  if (command === "import") return importCommand(rest);
  if (command === "setup") {
    return runSetup(rest, {
      cwd: process.cwd(),
      env: process.env,
      io: {
        input: process.stdin,
        output: process.stdout,
        isTty: Boolean(process.stdin.isTTY),
      },
      log: console.log,
      error: console.error,
      detect: defaultDetectSeams,
    });
  }
  console.error(USAGE);
  return 2;
}

process.exitCode = await main(process.argv.slice(2));

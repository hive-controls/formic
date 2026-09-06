/**
 * Heal CLI orchestration behind argv, so the exit-code contract and the rendering
 * are testable without a browser or a model.
 *
 *   0  passed (no repair needed) or healed (repair verified by a green replay)
 *   1  needs-human (an assertion proposal awaits review) or unhealed
 *   2  the harness could not start, the healer failed, or evidence could not be written
 *   3  preview-unavailable (the app host stopped serving before healing — an
 *      environment fault, not a heal verdict)
 */
import { untestedComponents } from "../evidence/coverage.mts";
import {
  hostnameOf,
  isLoopbackHost,
  redactPreviewText,
  scrubBackendIdentity,
  scrubError,
} from "../replay/evidence.mts";
import {
  unrebaseSpec,
  unrebaseText,
  type AppHostRebase,
} from "../host/rebase.mts";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { Driver } from "../driver/types.mts";
import type { ExitCode } from "../replay/cli.mts";
import { saveSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import type { StatusWriter } from "../status/stream.mts";
import {
  heal,
  type HealOptions,
  type HealOutcome,
  type HealResult,
} from "./loop.mts";
import type { Healer } from "./types.mts";

export interface HealCliIo {
  log(line: string): void;
  error(line: string): void;
}

/** Coverage is read off the GREEN record: the verified repair's replay when there is
 *  one, else the initial run (which, when it failed, covers only the steps it reached). */
export function coverageRecord(result: HealResult) {
  const verified = [...result.attempts]
    .reverse()
    .find((a) => a.verification?.result.outcome === "passed");
  return verified?.verification?.evidence ?? result.initial.evidence;
}

export type { AppHostRebase } from "../host/rebase.mts";

/** Restore the captured origin everywhere in a heal result: the spec structurally, and
 *  every string in the evidence (segments, failure text, reasons) textually. The result
 *  is plain data, so a JSON round trip is the whole-object rewrite. */
export function scrubHealResult(
  result: HealResult,
  appHost: AppHostRebase,
): HealResult {
  const unrebased = {
    ...result,
    spec: unrebaseSpec(result.spec, appHost.baseUrl, appHost.originalOrigin),
  };
  const unrebasedResult = JSON.parse(
    unrebaseText(
      JSON.stringify(unrebased),
      appHost.baseUrl,
      appHost.originalOrigin,
    ),
  ) as HealResult;
  // unrebaseText only rewrites a FULL matching URL; a bare mention of the hosted
  // preview's own hostname (no scheme — inside a healer's own reasoning, or a
  // Playwright timeout message) survives it untouched. One more pass, over every
  // string in the whole result, catches that — BEFORE the app-host row is restored
  // below, since that row is deliberately exempt from both passes.
  const scrubbed = scrubBackendIdentity(unrebasedResult, {
    hosts: [hostnameOf(appHost.baseUrl)],
  });
  // The audit's app-host row says WHERE the app ran; it is redacted at the source
  // (redactPreviewUrl) and must survive the rewrite, not read as the captured origin.
  scrubbed.initial.evidence.host = result.initial.evidence.host;
  scrubbed.attempts.forEach((attempt, i) => {
    const original = result.attempts[i].verification?.evidence;
    if (attempt.verification && original) {
      attempt.verification.evidence.host = original.host;
    }
  });
  return scrubbed;
}

/**
 * An error message fit to print: preview hosts redacted, and this run's resolved values
 * taken out too.
 *
 * These two sites run AFTER `heal()` has returned, so the loop's own outer scrub is
 * behind them — an evidence writer or a repair-PR step that fails while handling a step
 * the run resolved from the environment is on its own. The result is still in hand and
 * still knows what it resolved, so the same rule applies here.
 */
function printableError(
  error: unknown,
  result: HealResult,
  hosts: string[],
): string {
  const scrubbed = scrubError(error, {
    secrets: result.initial.result.resolvedSecrets,
  });
  return redactPreviewText((scrubbed as Error).message, { hosts });
}

export interface HealCliArgs {
  spec: Spec;
  driver: Driver;
  healer: Healer;
  io: HealCliIo;
  status?: StatusWriter;
  options?: HealOptions;
  /** Directory for evidence JSON (initial run + each attempt). */
  evidenceDir?: string;
  /** Called with the spec to commit when the outcome changed it. */
  writeSpec?: (yaml: string) => void;
  /** Runs after a completed heal (e.g. opening the repair PR). Its failure is
   *  operational — exit 2 — and never changes the heal's verdict. `recordArtifact`
   *  is a side channel for an artifact produced BEFORE a later step in the same
   *  callback fails (e.g. a bundle written to disk before a PR-open call throws):
   *  call it as soon as something durable exists, so the terminal event still
   *  names it even though the callback itself never returns normally. */
  afterHeal?: (
    result: HealResult,
    io: HealCliIo,
    recordArtifact: (path: string) => void,
  ) => Promise<string | undefined | void>;
  /** The `--app` host the spec was rebased onto. When set, the repaired spec is
   *  unrebased before write-back and the WHOLE result — evidence, segments, failure
   *  text, healer reasons — is scrubbed of the host's URL and token before anything
   *  (evidence files, the bundle, the PR body) reads it. See `scrubHealResult`. */
  appHost?: AppHostRebase;
  /** Where the untested-component proposals will be readable after this run (a bundle
   *  path, the repair PR, an evidence dir). Unset = tell the user how to get them. */
  proposalsHint?: (result: HealResult) => string | undefined;
  /** Run after everything else (evidence, writeSpec, afterHeal), before the terminal
   *  status event — the caller's own resource (e.g. a `--app` hosted app) so it is
   *  fully torn down before ok/failed/needs-human is reported. A failure here
   *  downgrades the exit code (and terminal event) to the harness-fault 2/failed. */
  cleanup?: () => Promise<void>;
}

export function exitCodeFor(outcome: HealOutcome): ExitCode {
  if (outcome === "preview-unavailable") return 3;
  return outcome === "passed" || outcome === "healed" ? 0 : 1;
}

/** `"harness-error"` stands for an operational failure (evidence write, writeSpec,
 *  afterHeal, or cleanup) that happened AFTER a heal outcome was already computed —
 *  it always reports as `failed`, superseding whatever the heal itself concluded,
 *  because the exit code it pairs with (2) is never a heal verdict. */
function emitTerminalStatus(
  status: StatusWriter | undefined,
  outcome: HealOutcome | "harness-error",
  artifact?: string,
): void {
  const event =
    outcome === "passed" || outcome === "healed"
      ? "ok"
      : outcome === "needs-human"
        ? "needs-human"
        : "failed";
  status?.emit(artifact ? { event, artifact } : { event });
}

/** The single point the terminal status event is emitted from: after the caller's
 *  own cleanup (if any) has run, so ok/failed/needs-human already agrees with
 *  whatever that cleanup left behind. */
async function closeOut(
  args: HealCliArgs,
  outcome: HealOutcome | "harness-error",
  artifact: string | undefined,
  exitCode: ExitCode,
): Promise<ExitCode> {
  if (args.cleanup) {
    try {
      await args.cleanup();
    } catch (cleanupError) {
      args.io.error(`cleanup failed: ${(cleanupError as Error).message}`);
      outcome = "harness-error";
      exitCode = 2;
    }
  }
  emitTerminalStatus(args.status, outcome, artifact);
  return exitCode;
}

/** The non-loopback hostnames of the specs in play (input and healed) — handed to
 *  redactPreviewText so a preview host named WITHOUT a URL scheme is still scrubbed. */
function previewHostsOf(...specs: Spec[]): string[] {
  const hosts = new Set<string>();
  for (const spec of specs) {
    try {
      const host = new URL(spec.startUrl).hostname.replace(/^\[|\]$/g, "");
      if (!isLoopbackHost(host)) hosts.add(host);
    } catch {
      // A startUrl that does not parse names no host to scrub.
    }
  }
  return [...hosts];
}

/** Longest-common-subsequence line diff; specs are tens of lines, the table is nothing. */
function lineDiff(a: string[], b: string[]): string[] {
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () =>
    new Array<number>(b.length + 1).fill(0),
  );
  for (let i = a.length - 1; i >= 0; i--) {
    for (let j = b.length - 1; j >= 0; j--) {
      lcs[i][j] =
        a[i] === b[j]
          ? lcs[i + 1][j + 1] + 1
          : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }
  const lines: string[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      i++;
      j++;
    } else if (lcs[i + 1][j] >= lcs[i][j + 1]) {
      lines.push(`- ${a[i++]}`);
    } else {
      lines.push(`+ ${b[j++]}`);
    }
  }
  while (i < a.length) lines.push(`- ${a[i++]}`);
  while (j < b.length) lines.push(`+ ${b[j++]}`);
  return lines;
}

/** The serialised spec split into its header and one line-block per step id. */
function blocksByStep(spec: Spec): {
  header: string[];
  steps: Map<string, string[]>;
} {
  const lines = saveSpec(spec).split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  const header: string[] = [];
  const steps = new Map<string, string[]>();
  let current: string[] | undefined;
  for (const line of lines) {
    const start = /^ {2}- id: (.+)$/.exec(line);
    if (start) {
      current = [line];
      steps.set(start[1].trim(), current);
    } else if (current) {
      current.push(line);
    } else {
      header.push(line);
    }
  }
  return { header, steps };
}

/**
 * The diff a reviewer reads, at STEP granularity, keyed by stable id.
 *
 * A plain line diff is wrong here in both directions it can be wrong: a greedy one
 * printed a whole step as deleted when an assertion proposal was recorded above it,
 * and a minimal (LCS) one aligned an inserted step's identical lines with the step
 * after it and showed a replacement — both measured on live heals, both the
 * "artifact shows the wrong thing" class. Steps are identities, so: an unchanged step
 * is silent, an inserted step is one added block, a changed step shows only its
 * changed lines, and a removed step is one removed block.
 */
export function renderSpecDiff(before: Spec, after: Spec): string[] {
  const x = blocksByStep(before);
  const y = blocksByStep(after);
  const out = lineDiff(x.header, y.header);
  for (const [id, lines] of y.steps) {
    const previous = x.steps.get(id);
    if (!previous) out.push(...lines.map((l) => `+ ${l}`));
    else out.push(...lineDiff(previous, lines));
  }
  for (const [id, lines] of x.steps) {
    if (!y.steps.has(id)) out.push(...lines.map((l) => `- ${l}`));
  }
  return out;
}

function summarize(
  result: HealResult,
  io: HealCliIo,
  proposalsHint?: string,
  hosts: string[] = [],
): void {
  const initial = result.initial.result;
  io.log(
    `${result.spec.name}: ${result.outcome.toUpperCase()} (healer ${result.healer.name}, ${result.healer.modelVersion})`,
  );
  if (result.outcome === "preview-unavailable") {
    // One redacted line, not a reasoning dump: nothing was attempted, so there is
    // no healer output to print — only the verdict and what to do about it.
    io.log(
      "  preview unavailable — the app host stopped serving before healing; no healer attempt was spent",
    );
    return;
  }
  if (initial.failure) {
    io.log(
      `  initial run failed at step ${initial.failure.index} (${initial.failure.phase}): ${redactPreviewText(initial.failure.error.split("\n")[0], { hosts })}`,
    );
  }
  const untested = untestedComponents(coverageRecord(result));
  if (untested.length > 0) {
    io.log(
      `  untested components: ${untested.length} (${untested
        .slice(0, 3)
        .map((c) => `${c.role} "${c.name}"`)
        .join(
          ", ",
        )}${untested.length > 3 ? ", …" : ""}) — ${proposalsHint ? `proposed steps in ${proposalsHint}` : "pass --bundle <dir> (or --evidence <dir>) to write proposals.json"}`,
    );
  }
  for (const attempt of result.attempts) {
    const verdict = attempt.verification
      ? attempt.verification.result.outcome === "passed"
        ? "verified"
        : `rejected: ${redactPreviewText(attempt.verification.result.failure?.error.split("\n")[0] ?? "", { hosts })}`
      : attempt.proposal.kind === "propose-assert-change"
        ? "for human review"
        : "declined";
    io.log(
      `  attempt ${attempt.attempt}: ${attempt.proposal.kind} — ${redactPreviewText(attempt.proposal.reason, { hosts })} → ${verdict}`,
    );
  }
}

export async function runHealCli(args: HealCliArgs): Promise<ExitCode> {
  args.status?.emit({ event: "progress" });
  // Computed up front so even a heal that THROWS can have its error scrubbed of the
  // preview host it names.
  const inputHosts = previewHostsOf(args.spec);
  let result: HealResult;
  try {
    result = await heal(args.spec, args.driver, args.healer, args.options);
  } catch (error) {
    args.io.error(
      `heal failed: ${redactPreviewText((error as Error).message, { hosts: inputHosts })}`,
    );
    return closeOut(args, "harness-error", undefined, 2);
  }
  // The healed spec's startUrl may differ from the input's; both hosts are scrubbed.
  const hosts = previewHostsOf(args.spec, result.spec);
  if (args.appHost) {
    result = scrubHealResult(result, args.appHost);
  }
  summarize(result, args.io, args.proposalsHint?.(result), hosts);

  const diffBase = args.appHost
    ? unrebaseSpec(args.spec, args.appHost.baseUrl, args.appHost.originalOrigin)
    : args.spec;
  const diff = renderSpecDiff(diffBase, result.spec);
  if (diff.length > 0) {
    args.io.log("  spec diff:");
    // A proposal can carry a tokened preview URL INTO the spec (an inserted goto, a
    // proposed assertion's hasText) — the printed diff is stdout too.
    for (const line of diff)
      args.io.log(`    ${redactPreviewText(line, { hosts })}`);
  }

  let artifact: string | undefined;
  let outcome: HealOutcome | "harness-error" = result.outcome;
  let exitCode: ExitCode = exitCodeFor(result.outcome);
  try {
    if (args.evidenceDir) {
      mkdirSync(args.evidenceDir, { recursive: true });
      const write = (name: string, value: unknown) =>
        writeFileSync(
          join(args.evidenceDir!, name),
          JSON.stringify(value, null, 2),
        );
      write("initial.json", result.initial.evidence);
      write("proposals.json", {
        untestedComponents: untestedComponents(coverageRecord(result)),
      });
      result.attempts.forEach((attempt, i) =>
        write(`attempt-${i + 1}.json`, {
          proposal: attempt.proposal,
          before: attempt.before,
          after: attempt.after,
          verification: attempt.verification?.evidence ?? null,
        }),
      );
      args.io.log(`  evidence -> ${args.evidenceDir}`);
      artifact = args.evidenceDir;
    }
    if (args.writeSpec && diff.length > 0) {
      args.writeSpec(saveSpec(result.spec));
      args.io.log("  spec written");
    }
  } catch (error) {
    args.io.error(`evidence failed: ${printableError(error, result, hosts)}`);
    outcome = "harness-error";
    exitCode = 2;
  }
  // An environment fault is not a repair: afterHeal (the repair PR under --pr) must
  // not run for preview-unavailable. Nor after an evidence/writeSpec failure above —
  // there is nothing left to hand a repair PR that hasn't already failed.
  if (
    outcome !== "harness-error" &&
    args.afterHeal &&
    result.outcome !== "preview-unavailable"
  ) {
    try {
      const postHealArtifact = await args.afterHeal(result, args.io, (path) => {
        artifact = path;
      });
      if (postHealArtifact) artifact = postHealArtifact;
    } catch (error) {
      args.io.error(
        `post-heal step failed: ${printableError(error, result, hosts)}`,
      );
      outcome = "harness-error";
      exitCode = 2;
    }
  }
  return closeOut(args, outcome, artifact, exitCode);
}

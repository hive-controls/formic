/**
 * The replay CLI's orchestration, separated from argv so the exit-code contract is
 * unit-testable with a fake driver.
 *
 * Exit codes are the contract a CI caller routes on:
 *   0  the replay ran and passed
 *   1  the replay ran and FAILED — a step broke or an assertion did not hold
 *   2  anything else: the session could not open, the evidence stage failed, or
 *      the session could not be closed. None of those is a test failure, and
 *      reporting them as 1 would tell a caller the product is broken when the
 *      harness is.
 */
import type { Driver, DriverSession } from "../driver/types.mts";
import type { Spec } from "../spec/types.mts";
import type { StatusWriter } from "../status/stream.mts";
import { unrebaseText, type AppHostRebase } from "../host/rebase.mts";
import {
  assembleEvidence,
  scrubError,
  scrubEvidenceRecord,
  tooShortToScrub,
  SECRET_MIN_FREE_TEXT_LENGTH,
  type ResolvedSecret,
} from "./evidence.mts";
import { replaySpec, type ReplayResult } from "./runner.mts";

/** The shared CLI exit-code union; replay itself never emits 3 — that code is the
 *  heal CLI's `preview-unavailable` (see heal/cli.mts's header). */
export type ExitCode = 0 | 1 | 2 | 3;

export interface CliIo {
  log(line: string): void;
  error(line: string): void;
}

export interface ReplayCliArgs {
  spec: Spec;
  driver: Driver;
  evidenceFile?: string;
  /** Set when `--app` hosted the app for this run. Already redacted. */
  host?: { name: string; kind: "Outside" | "Inside"; baseUrl: string } | null;
  /** The host the spec was rebased onto: every printed line and the written record
   *  are unrebased through it, so a tokened preview URL never reaches a log. */
  appHost?: AppHostRebase;
  io: CliIo;
  status?: StatusWriter;
  /** Injectable so a failing write is testable without touching the filesystem. */
  writeEvidence: (file: string, json: string) => void;
  /** Run after the driver session is closed, before the terminal status event — the
   *  caller's own resource (e.g. a `--app` hosted app) so it is fully torn down
   *  before ok/failed is reported. A failure here never hides the replay's own
   *  verdict from stdout, but downgrades the exit code (and terminal event) to the
   *  harness-fault 2/failed, since an "ok" the caller cannot act on is worse than
   *  none. */
  cleanup?: () => Promise<void>;
}

function printResult(
  result: ReplayResult,
  driverName: string,
  io: CliIo,
  appHost?: AppHostRebase,
) {
  const clean = (text: string) =>
    appHost
      ? unrebaseText(text, appHost.baseUrl, appHost.originalOrigin)
      : text;
  io.log(
    `${result.specName}: ${result.outcome.toUpperCase()} on ${driverName}`,
  );
  for (const step of result.steps) {
    io.log(
      `  ${step.index} ${step.action.padEnd(6)} ${clean(step.target ?? "").padEnd(28)} ${step.endedAt - step.startedAt}ms ${step.outcome}`,
    );
  }
  if (result.failure) {
    io.log(
      `  step ${result.failure.index} failed in ${result.failure.phase} phase:\n    ${clean(result.failure.error).split("\n").join("\n    ")}`,
    );
  }
}

/**
 * Says, once, that a resolved value was too short to remove from free text.
 *
 * Below the floor a value is not substituted out of prose, because doing so corrupts
 * the record it protects (see SECRET_MIN_FREE_TEXT_LENGTH). The step log still withholds
 * it structurally — its position is known there — but an error message quoting the
 * field's contents would carry it into the terminal and the evidence, and that is a
 * real reduction in what the run can promise. A guarantee that quietly narrows is the
 * one a reviewer will not know to stop relying on.
 */
function warnAboutShortSecrets(result: ReplayResult, io: CliIo): void {
  for (const secret of result.resolvedSecrets ?? []) {
    if (!tooShortToScrub(secret.value)) continue;
    io.log(
      `  warning: ${secret.reference} resolved to a value under ${SECRET_MIN_FREE_TEXT_LENGTH} characters — too short to redact from free text without corrupting it, so it is withheld from the step log but may appear in an error message or a snapshot`,
    );
  }
}

async function evidenceStage(
  result: ReplayResult,
  session: DriverSession,
  args: ReplayCliArgs,
): Promise<string | undefined> {
  // On Solari this releases the session — it must come after the run, never during.
  const events = await session.fetchReplay();
  const assembled = assembleEvidence(result, events, {
    driver: args.driver.name,
    sessionId: session.sessionId,
    host: args.host,
    cdpConnect: session.cdpConnect,
    // The replay stream is a SECOND channel out of the page and it carries typed
    // values; the runner scrubbed what it returned, but it never saw these events.
    secrets: result.resolvedSecrets,
  });
  const evidence = scrubEvidenceRecord(assembled, args.appHost);
  args.io.log(
    evidence.recording === "captured"
      ? `  evidence: ${evidence.segments.length} replay segment(s) (decision ${evidence.decisionId})`
      : `  evidence: step log only — ${args.driver.name} cannot record a replay stream (decision ${evidence.decisionId})`,
  );
  if (args.evidenceFile) {
    args.writeEvidence(args.evidenceFile, JSON.stringify(evidence, null, 2));
    args.io.log(`  evidence -> ${args.evidenceFile}`);
    return args.evidenceFile;
  }
  return undefined;
}

/** The single point the terminal status event is emitted from: after the caller's
 *  own cleanup (if any) has run, so an ok/failed already agrees with whatever that
 *  cleanup left behind. A cleanup failure downgrades to the harness-fault 2/failed
 *  without discarding an artifact already produced. */
async function closeOut(
  args: ReplayCliArgs,
  exitCode: ExitCode,
  artifact: string | undefined,
): Promise<ExitCode> {
  if (args.cleanup) {
    try {
      await args.cleanup();
    } catch (cleanupError) {
      args.io.error(`cleanup failed: ${(cleanupError as Error).message}`);
      exitCode = 2;
    }
  }
  const event = exitCode === 0 ? "ok" : "failed";
  args.status?.emit(artifact ? { event, artifact } : { event });
  return exitCode;
}

export async function runReplayCli(args: ReplayCliArgs): Promise<ExitCode> {
  args.status?.emit({ event: "progress" });
  let session: DriverSession;
  try {
    session = await args.driver.open();
  } catch (setupError) {
    args.io.error(`could not start: ${(setupError as Error).message}`);
    return closeOut(args, 2, undefined);
  }

  let exitCode: ExitCode = 2;
  let artifact: string | undefined;
  // Hoisted: every message printed from here on is printed by a catch that has no
  // result in scope, and an error raised while writing evidence for a step the run
  // resolved from the environment is exactly the one that would quote it.
  let resolved: ResolvedSecret[] | undefined;
  const printable = (error: unknown): string =>
    (scrubError(error, { secrets: resolved }) as Error).message;
  try {
    const result = await replaySpec(args.spec, session);
    resolved = result.resolvedSecrets;
    printResult(result, args.driver.name, args.io, args.appHost);
    warnAboutShortSecrets(result, args.io);
    // The verdict is settled here. Everything after is operational.
    exitCode = result.outcome === "passed" ? 0 : 1;
    try {
      artifact = await evidenceStage(result, session, args);
    } catch (evidenceError) {
      args.io.error(`evidence failed: ${printable(evidenceError)}`);
      exitCode = 2;
    }
  } finally {
    try {
      await session.close();
    } catch (closeError) {
      // A session that will not close (a Solari release that keeps failing) is a
      // harness problem, not a test verdict — and it may be a still-billing session.
      args.io.error(`session close failed: ${printable(closeError)}`);
      exitCode = 2;
    }
  }
  return closeOut(args, exitCode, artifact);
}

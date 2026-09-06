/**
 * `record`'s orchestration, separated from argv so it is testable with a fake driver.
 *
 * Exit codes follow the replay CLI's contract, one meaning per code:
 *   0  a spec was written
 *   1  nothing to write — the human recorded no actions, or skipped a proposal a
 *      state-changing step needs, so the spec would not load
 *   2  the harness failed: no session, no page, the file could not be written
 *
 * The confirmation pass is the whole reason 1 exists as a distinct outcome. A recording
 * derives its locators and PROPOSES its expectations; a proposal the human declines
 * leaves that step bare, and a bare state-changing step is a spec the validator refuses.
 * Reporting that as a harness failure would hide a human decision inside an error.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import type { Driver, DriverSession } from "../driver/types.mts";
import type { ExitCode, CliIo } from "../replay/cli.mts";
import { recordFlow, type RecordedFlow } from "../capture/record.mts";
import {
  SENSITIVE_TABLE,
  classifySensitiveValue,
} from "../capture/sensitive-fields.mts";
import {
  applyProposals,
  type AssertionProposal,
} from "../capture/proposals.mts";
import { SpecValidationError, saveSpec, validateSpec } from "../spec/parse.mts";
import type { Assertion } from "../spec/types.mts";

export interface RecordCliArgs {
  specName: string;
  /** Where the recording starts — the spec's first `goto`. */
  startUrl: string;
  driver: Driver;
  outFile: string;
  /** Resolves when the human ends the recording. */
  stop: Promise<void>;
  /** Give up the wait behind `stop` — called on EVERY exit path, so a run that never
   *  reached the browser still restores the terminal it took over. */
  cancelStop?: () => void;
  /** Record password values verbatim instead of `<secret>`. */
  includeSecrets?: boolean;
  /** Whether the human handed the answer over in advance (`--yes`). It is not the same
   *  question as which function `confirm` is: what matters is that NOBODY is reading the
   *  proposals as they go past. */
  assumeYes?: boolean;
  /** Asked once per proposal, in order. `--yes` answers true without asking. */
  confirm: (proposal: AssertionProposal) => Promise<boolean>;
  io: CliIo;
  /** Injectable so a failing write is testable without touching the filesystem. */
  writeSpec: (file: string, yaml: string) => void;
}

function printSteps(flow: RecordedFlow, driverName: string, io: CliIo): void {
  io.log(`${flow.spec.name}: ${flow.steps.length} step(s) on ${driverName}`);
  for (const step of flow.steps) {
    io.log(
      `  ${step.index} ${step.action.padEnd(6)} ${(step.target ?? "").padEnd(28)} ${step.endedAt - step.startedAt}ms ${step.outcome}`,
    );
  }
}

/** Everything the recording could NOT represent. A refusal only the recorder knows
 *  about is a spec silently missing an action the human performed. */
function warnAboutRefusals(flow: RecordedFlow, io: CliIo): void {
  for (const warning of flow.warnings) io.log(`  warning: ${warning}`);
}

/**
 * One line per withheld value, then one line naming every variable.
 *
 * It has to be said out loud, naming the step, the category AND the variable: a spec
 * whose sign-in reads `valueFrom: env.SIGN_IN_PASSWORD` replays as a failed login with
 * no explanation unless the person who recorded it knows the value was withheld by
 * design and knows which variable to put it in.
 */
function warnAboutSecrets(flow: RecordedFlow, io: CliIo): void {
  for (const secret of flow.secrets) {
    io.log(
      `  warning: step ${secret.index} (${secret.target}) holds ${secret.category} data — the spec references env.${secret.variable} instead of the value`,
    );
  }
  // ONE line, at the end, listing every variable. The per-step warnings scroll past in
  // a long recording; this is the line a person copies into their .env, and without it
  // the spec's first replay fails on a missing variable they were never told about.
  if (flow.secrets.length > 0) {
    io.log(
      `  replay needs these environment variables: ${flow.secrets
        .map((secret) => secret.variable)
        .join(", ")}`,
    );
  }
}

/** Every string an assertion would commit to the spec, in the order a reader meets them. */
function assertedText(assertion: Assertion): string[] {
  return [
    assertion.hasText,
    assertion.containsText,
    assertion.name,
    assertion.text,
  ].filter((one): one is string => one !== undefined && one !== "");
}

/**
 * Ask about every proposal, in step order, and return the ones adopted.
 *
 * The recorder withholds what the human TYPES, but an application that renders the
 * signed-in user's own address puts personal data back on screen where no field decision
 * can reach it — and the proposal built from that screen would commit it as `hasText`.
 * Interactively that line is read out and the human can decline, which is the control.
 * `--yes` answers for them, so there the proposal is DROPPED and named rather than
 * adopted silently. `--include-secrets` is the human saying otherwise, out loud.
 *
 * Dropping the ONLY candidate for a step used to cost the whole recording: the step had
 * nothing left to assert, the validator refused, and protecting the address threw the
 * spec away with it. So the drop SUBSTITUTES rather than merely deletes — the step
 * asserts something about its OWN target, the locator capture already proved, never a
 * new unproven one.
 *
 * Which thing depends on what the page SAW, not on what would be convenient. Asserting
 * the target is visible is false exactly where the stand-in is needed, because a
 * state-changing click usually removes the thing it clicked — measured: a spec doing
 * that failed on replay at the sign-in button it had just recorded. So a target the page
 * saw go away is asserted GONE, which is both true and a stronger statement that the
 * step did something; a target still there is asserted visible.
 */
async function confirmationPass(
  flow: RecordedFlow,
  args: RecordCliArgs,
): Promise<AssertionProposal[]> {
  const accepted: AssertionProposal[] = [];
  const unread = args.assumeYes === true && args.includeSecrets !== true;
  for (const proposal of flow.proposals) {
    const personal = unread
      ? assertedText(proposal.assertion)
          .map((text) => classifySensitiveValue(text, SENSITIVE_TABLE))
          .find((found) => found !== null)
      : undefined;
    if (personal) {
      const fallback = onlyCandidateFallback(flow, proposal);
      args.io.log(
        `  warning: proposal for step ${proposal.stepId} looks like ${personal.category} data and nobody is reading — skipped: ${proposal.summary}` +
          (fallback === null
            ? ""
            : `; fell back to asserting ${describeFallback(fallback.assertion)}`),
      );
      if (fallback !== null) accepted.push(fallback);
      continue;
    }
    if (await args.confirm(proposal)) accepted.push(proposal);
  }
  args.io.log(
    `  assertions: ${accepted.length} of ${flow.proposals.length} proposal(s) accepted`,
  );
  return accepted;
}

/** Which of the three stand-ins was used, in the words the warning uses. */
function describeFallback(assertion: Assertion): string {
  if (assertion.url !== undefined) return `the url is ${assertion.url}`;
  return `the target is ${assertion.visible === true ? "visible" : "gone"}`;
}

/**
 * The stand-in for a dropped proposal, or null when there is nothing to stand in for —
 * another proposal already covers the step, or the step has no target of its own.
 *
 * The locator is the step's `target` verbatim, which capture proved. The polarity is
 * what the PAGE reported become of it, never a guess made here.
 */
function onlyCandidateFallback(
  flow: RecordedFlow,
  dropped: AssertionProposal,
): AssertionProposal | null {
  const others = flow.proposals.filter(
    (one) => one.stepId === dropped.stepId && one !== dropped,
  );
  if (others.length > 0) return null;
  const step = flow.spec.steps.find((one) => one.id === dropped.stepId);
  if (step?.target === undefined || step.action === "goto") return null;
  // A step that changed the DOCUMENT is best described by the document it reached: that
  // says what the step did, where "the button is gone" only says what it stopped being.
  // A navigation is a page-level fact and the grammar has a page-level assertion for it,
  // carrying exactly one url key whose value is what the page itself reported — already
  // absolute and canonical, which is what the parser requires.
  if (dropped.urlAfter !== undefined) {
    return {
      stepId: dropped.stepId,
      basis: dropped.basis,
      targetAfter: dropped.targetAfter,
      urlAfter: dropped.urlAfter,
      summary: `the page is at ${dropped.urlAfter}`,
      assertion: { url: dropped.urlAfter },
    };
  }
  const stillThere = dropped.targetAfter !== "gone";
  return {
    stepId: dropped.stepId,
    basis: dropped.basis,
    targetAfter: dropped.targetAfter,
    summary: `${step.target} is ${stillThere ? "visible" : "gone"}`,
    assertion: { selector: step.target, visible: stillThere },
  };
}

/** The confirmation pass and the write, as one outcome. Keeping it out of `runRecordCli`
 *  is what lets that function have a single return after cleanup. */
async function commitSpec(
  flow: RecordedFlow,
  args: RecordCliArgs,
): Promise<ExitCode> {
  // ACTIONS, not proposals. A goto has no proposal by design — the grammar exempts it,
  // and proposing an expectation for a navigation the human typed would ask them to
  // confirm what they just did — so equating the two refused to write the perfectly
  // valid spec a navigation-only recording had already produced.
  if (flow.spec.steps.length <= 1) {
    args.io.error("nothing recorded — no spec written");
    return 1;
  }
  const spec = applyProposals(flow.spec, await confirmationPass(flow, args));
  try {
    validateSpec(spec);
  } catch (invalid) {
    const problems =
      invalid instanceof SpecValidationError
        ? invalid.problems
        : [(invalid as Error).message];
    args.io.error(
      `not written — a declined proposal left a step with nothing to assert:\n  - ${problems.join("\n  - ")}`,
    );
    return 1;
  }
  args.writeSpec(args.outFile, saveSpec(spec));
  args.io.log(`  spec -> ${args.outFile}`);
  return 0;
}

export async function runRecordCli(args: RecordCliArgs): Promise<ExitCode> {
  let session: DriverSession;
  try {
    session = await args.driver.open();
  } catch (setupError) {
    args.io.error(`could not start: ${(setupError as Error).message}`);
    args.cancelStop?.();
    return 2;
  }

  // ONE return, after cleanup. An early return fixes the exit code before `finally`
  // runs, so a session that failed to close — which may still be billing — was reported
  // in the log and then contradicted by a success exit. The code a caller routes on has
  // to be the last word about the whole run, cleanup included.
  let exitCode: ExitCode = 2;
  try {
    const flow = await recordFlow({
      session,
      specName: args.specName,
      driverName: args.driver.name,
      startUrl: args.startUrl,
      stop: args.stop,
      includeSecrets: args.includeSecrets,
    });
    printSteps(flow, args.driver.name, args.io);
    warnAboutRefusals(flow, args.io);
    warnAboutSecrets(flow, args.io);
    exitCode = await commitSpec(flow, args);
  } catch (recordError) {
    args.io.error(`recording failed: ${(recordError as Error).message}`);
    exitCode = 2;
  } finally {
    try {
      await session.close();
    } catch (closeError) {
      // A session that will not close may still be billing — a harness problem, and
      // never something to report as a recording the human got wrong.
      args.io.error(`session close failed: ${(closeError as Error).message}`);
      exitCode = 2;
    }
    args.cancelStop?.();
  }
  return exitCode;
}

/**
 * Whether an answer to a proposal prompt ADOPTS it. Only `y` or `yes`, in any case.
 *
 * Every other answer declines, a blank line included, and the prompt says so. It used
 * to test "does the answer start with n", which made `maybe` an approval and a stray
 * keypress an approval too. Adopting an expectation is the human's call, and the two
 * mistakes are not symmetric: a declined proposal announces itself immediately (the
 * spec refuses to write and names the bare step), while one adopted by accident never
 * announces itself at all.
 */
export function acceptsProposal(answer: string): boolean {
  const said = answer.trim().toLowerCase();
  return said === "y" || said === "yes";
}

/**
 * Write a spec, creating the directory it goes in.
 *
 * The default out path is `specs/<name>.yaml`, and a first recording is exactly when
 * that directory does not exist yet — so the command that exists to produce a project's
 * first spec failed on the project that had never had one.
 */
export function writeSpecFile(file: string, yaml: string): void {
  mkdirSync(dirname(resolve(file)), { recursive: true });
  writeFileSync(file, yaml);
}

/** A key-press wait that can be given up on. */
export interface KeyPressWait {
  pressed: Promise<void>;
  /** Restore the terminal and stop listening, whether or not a key ever came. */
  cancel(): void;
}

/**
 * Resolve on the human's next key press. Raw mode is what makes it ONE key rather than
 * a line: a recording ends when the human is done clicking, and reaching for Enter is
 * one more interaction to explain. A non-tty stdin (a pipe, CI) has no raw mode, so it
 * ends on the first byte instead — the same contract, minus the terminal.
 *
 * CANCELLABLE, because the wait starts before the browser does. A gate that fails to
 * open used to leave stdin resumed and in raw mode with a listener still attached: the
 * terminal echoed nothing afterwards and the process had a reason to stay alive. Every
 * exit path — key pressed, run finished, run failed — goes through the same restore.
 */
export function waitForKeyPress(input: NodeJS.ReadStream): KeyPressWait {
  const rawCapable = Boolean(input.isTTY);
  let restore = () => {};
  const pressed = new Promise<void>((resolve) => {
    const onData = () => restore();
    restore = () => {
      restore = () => {};
      input.removeListener("data", onData);
      if (rawCapable) input.setRawMode(false);
      input.pause();
      resolve();
    };
    if (rawCapable) input.setRawMode(true);
    input.resume();
    input.once("data", onData);
  });
  return { pressed, cancel: () => restore() };
}

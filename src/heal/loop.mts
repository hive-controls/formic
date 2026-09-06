/**
 * The heal loop: replay → on failure, ask the healer → apply → replay again to VERIFY.
 *
 * Nothing a healer says counts until the whole spec replays green with its proposal
 * applied. That is the difference between "healed" and "patched": a locator repair
 * that makes the step pass but breaks the flow later is not a repair.
 *
 * Every run — the failing one and each verification — produces a full evidence record,
 * so the audit trail holds the BEFORE segment (the step as it failed) and the AFTER
 * segment (the step as repaired), which is what the repair PR embeds.
 */
import type { Driver } from "../driver/types.mts";
import type { EvidenceRecord, ReplaySegment } from "../evidence/types.mts";
import {
  assembleEvidence,
  scrubBackendIdentity,
  scrubError,
  withholdKnownValues,
  type ResolvedSecret,
} from "../replay/evidence.mts";
import { replaySpec, type ReplayResult } from "../replay/runner.mts";
import type { Spec } from "../spec/types.mts";
import {
  applyProposal,
  parseProposal,
  ProposalValidationError,
} from "./proposal.mts";
import type {
  HealContext,
  Healer,
  PriorAttempt,
  RepairProposal,
} from "./types.mts";
import {
  checkPreviewLiveness,
  type PreviewLivenessCheck,
} from "./preview-liveness.mts";

export interface HealOptions {
  /** Proposals tried before giving up. Each costs a full replay. */
  maxAttempts?: number;
  stepTimeoutMs?: number;
  /** Set when `--app` hosted the app for this run. Already redacted;
   *  carried into every run's (initial and each verification's) evidence record. */
  host?: { name: string; kind: "Outside" | "Inside"; baseUrl: string } | null;
  /** Probed against the spec's start URL before EACH healer attempt: a dead/expired
   *  preview fails the run fast as `preview-unavailable` instead of spending an
   *  attempt diagnosing an environment fault. Tests inject this seam; the default
   *  hits the network (loopback hosts are exempt inside the check itself). */
  previewLiveness?: PreviewLivenessCheck;
}

export interface RunRecord {
  result: ReplayResult;
  evidence: EvidenceRecord;
  /** Present when the run failed: the page as the healer saw it. */
  pageAtFailure?: { url: string; ariaSnapshot: string };
}

export interface HealAttempt {
  attempt: number;
  proposal: RepairProposal;
  /** The verification replay, when the proposal was applied and re-run. */
  verification?: RunRecord;
  /** The failed step as it failed, and the same (or inserted) step after repair. */
  before: ReplaySegment | null;
  after: ReplaySegment | null;
}

export type HealOutcome =
  "passed" | "healed" | "needs-human" | "unhealed" | "preview-unavailable";

export interface HealResult {
  outcome: HealOutcome;
  /** The spec to commit: repaired when healed; carrying proposedAssertChange when
   *  needs-human; the original otherwise. */
  spec: Spec;
  initial: RunRecord;
  attempts: HealAttempt[];
  healer: { name: string; modelVersion: string };
}

async function runOnce(
  spec: Spec,
  driver: Driver,
  options: HealOptions,
  modelVersion: string | null,
): Promise<RunRecord> {
  const session = await driver.open();
  try {
    // Scrubbed immediately: this same object seeds both the audit record
    // (assembleEvidence, below) and the healer's own context (contextFor, in the
    // outer loop) — the backend's session identity must never survive into either.
    const result = scrubBackendIdentity(
      await replaySpec(spec, session, {
        stepTimeoutMs: options.stepTimeoutMs,
      }),
      { sessionIds: [session.sessionId] },
    );
    let pageAtFailure: RunRecord["pageAtFailure"];
    if (result.outcome === "failed") {
      // Captured while the page is still live and before fetchReplay(), which on
      // Solari releases the session. This snapshot is taken AFTER the replay has
      // returned, so the runner never saw it — and an application that echoes what was
      // typed (a signed-in identity in a header) puts the resolved value on exactly
      // this screen, which is the one the healer is shown.
      pageAtFailure = scrubBackendIdentity(
        {
          url: session.page.url(),
          ariaSnapshot: await session.page
            .locator("body")
            .ariaSnapshot()
            .catch(() => "(accessibility snapshot unavailable)"),
        },
        {
          sessionIds: [session.sessionId],
          secrets: result.resolvedSecrets,
        },
      );
    }
    const events = await session.fetchReplay();
    const evidence = assembleEvidence(result, events, {
      driver: driver.name,
      sessionId: session.sessionId,
      modelVersion,
      host: options.host,
      cdpConnect: session.cdpConnect,
      secrets: result.resolvedSecrets,
    });
    return { result, evidence, pageAtFailure };
  } finally {
    await session.close();
  }
}

function segmentFor(record: RunRecord, stepId: string): ReplaySegment | null {
  return record.evidence.segments.find((s) => s.stepId === stepId) ?? null;
}

function contextFor(
  spec: Spec,
  run: RunRecord,
  attempt: number,
  priorAttempts: PriorAttempt[],
): HealContext {
  const failure = run.result.failure;
  if (!failure) throw new Error("contextFor called on a passing run");
  const failedStep = spec.steps.find((s) => s.id === failure.stepId);
  if (!failedStep) throw new Error(`failed step ${failure.stepId} not in spec`);
  return {
    spec,
    failure,
    failedStep,
    url: run.pageAtFailure?.url ?? "",
    ariaSnapshot: run.pageAtFailure?.ariaSnapshot ?? "",
    attempt,
    priorAttempts,
  };
}

/**
 * Everything a healer says, with this run's resolved values taken back out.
 *
 * The proposal is DATA the model wrote after reading the failing page, so it is an
 * untrusted string like any other — and it travels further than the evidence record
 * does: the CLI prints the reason, the bundle writes the whole proposal, the PR body
 * shows it, and it feeds back into the next attempt's brief as a prior attempt.
 */
function withoutSecrets<T>(value: T, run: RunRecord): T {
  const secrets = run.result.resolvedSecrets;
  // Two rules for two kinds of string. A reason is prose, where a short value is left
  // alone because substituting it corrupts what it is embedded in. A proposed step's
  // `value` is a FIELD that holds nothing but a typed value, so the floor has no reason
  // to apply there and a four-digit PIN is withheld like any other.
  return withholdKnownValues(scrubBackendIdentity(value, { secrets }), secrets);
}

/** Adds a run's resolved values to the list the outer boundary scrubs with. */
function remember(resolved: ResolvedSecret[], run: RunRecord): void {
  for (const secret of run.result.resolvedSecrets ?? []) {
    if (!resolved.some((known) => known.value === secret.value)) {
      resolved.push(secret);
    }
  }
}

/** Which step a verified proposal repaired — for the AFTER segment. */
function repairedStepId(
  original: Spec,
  repaired: Spec,
  proposal: RepairProposal,
): string {
  if (proposal.kind === "rewrite-target") return proposal.stepId;
  const originalIds = new Set(original.steps.map((s) => s.id));
  return repaired.steps.find((s) => !originalIds.has(s.id))?.id ?? "";
}

/**
 * The loop, with every escaping error scrubbed on the way out.
 *
 * Everything a healer RETURNS goes through the scrub already. What it THROWS did not,
 * and an exception is an artifact like any other: the CLI prints it, CI annotates it, a
 * log keeps it. A backend that fails parsing a model's reply raises a `SyntaxError`
 * naming the text it choked on — the page the healer was reading.
 *
 * Wrapped at the OUTER boundary rather than at the one rethrow that was found, because
 * the rule is about the boundary, not about that call: a driver that throws while
 * closing, an evidence assembly that refuses a segment, and every future throw inside
 * this function leave by the same door and are cleaned by the same pass.
 */
export async function heal(
  spec: Spec,
  driver: Driver,
  healer: Healer,
  options: HealOptions = {},
): Promise<HealResult> {
  const resolved: ResolvedSecret[] = [];
  try {
    return await healInner(spec, driver, healer, options, resolved);
  } catch (error) {
    throw scrubError(error, { secrets: resolved });
  }
}

async function healInner(
  spec: Spec,
  driver: Driver,
  healer: Healer,
  options: HealOptions,
  resolved: ResolvedSecret[],
): Promise<HealResult> {
  const maxAttempts = options.maxAttempts ?? 2;
  // Read at return time, not up front: an agent-backed healer learns its version on
  // its first proposal, and the audit record must carry the real one.
  const healerInfo = () => ({
    name: healer.name,
    modelVersion: healer.modelVersion,
  });

  const initial = await runOnce(spec, driver, options, null);
  // Collected as each run learns them, so an error thrown at ANY later point leaves
  // through a boundary that already knows what must not travel with it.
  remember(resolved, initial);
  const attempts: HealAttempt[] = [];
  if (initial.result.outcome === "passed") {
    return { outcome: "passed", spec, initial, attempts, healer: healerInfo() };
  }

  let currentSpec = spec;
  let lastRun = initial;
  const priorAttempts: PriorAttempt[] = [];
  const livenessCheck = options.previewLiveness ?? checkPreviewLiveness;

  for (let attempt = 1; attempt <= maxAttempts; attempt++) {
    // The pre-attempt liveness call site: a preview that died since the last run
    // (an expired sandbox 404s) is an environment fault, not a heal verdict — fail
    // fast rather than spend a healer attempt diagnosing it. The probe targets the
    // spec's startUrl on purpose: startUrl is the preview origin the sandbox serves,
    // so its liveness IS the question (not whichever page the failure landed on).
    const liveness = await livenessCheck(currentSpec.startUrl);
    if (!liveness.alive) {
      return {
        outcome: "preview-unavailable",
        spec,
        initial,
        attempts,
        healer: healerInfo(),
      };
    }
    // A snapshot, not the live array: the context is a record of what the healer
    // was shown at this attempt, and must not grow after the fact.
    const context = contextFor(currentSpec, lastRun, attempt, [
      ...priorAttempts,
    ]);
    const before = segmentFor(lastRun, context.failure.stepId);
    let proposal: RepairProposal;
    try {
      // Scrubbed on the way BACK. A healer reads the failing screen and can quote what
      // it saw into its own reasoning — which the CLI prints, the bundle writes and the
      // PR body shows — and an inserted step's literal value is the same door. Nothing
      // the model says reaches an artifact unfiltered.
      proposal = withoutSecrets(
        parseProposal(await healer.propose(context)),
        lastRun,
      );
    } catch (error) {
      if (!(error instanceof ProposalValidationError)) throw error;
      // A malformed proposal is a declined attempt, not a harness failure: the run's
      // evidence must still be written, and the healer gets the rejection fed back and
      // another attempt if the budget allows.
      const declined: RepairProposal = withoutSecrets(
        {
          kind: "no-repair",
          // The rejection quotes the model's own output back, so it is scrubbed too.
          reason: `the healer's proposal was invalid and was not applied — ${error.message}`,
        },
        lastRun,
      );
      attempts.push({ attempt, proposal: declined, before, after: null });
      priorAttempts.push({ proposal: declined, result: error.message });
      continue;
    }

    if (proposal.kind === "no-repair") {
      attempts.push({ attempt, proposal, before, after: null });
      break;
    }
    if (proposal.kind === "propose-assert-change") {
      // Recorded on the spec for a human; never verified because never applied.
      const annotated = applyProposal(currentSpec, proposal);
      attempts.push({ attempt, proposal, before, after: null });
      return {
        outcome: "needs-human",
        spec: annotated,
        initial,
        attempts,
        healer: healerInfo(),
      };
    }

    const candidate = applyProposal(currentSpec, proposal);
    const verification = await runOnce(
      candidate,
      driver,
      options,
      healer.modelVersion,
    );
    remember(resolved, verification);
    const after = segmentFor(
      verification,
      repairedStepId(currentSpec, candidate, proposal),
    );
    attempts.push({ attempt, proposal, verification, before, after });

    if (verification.result.outcome === "passed") {
      markHealed(
        verification.result,
        repairedStepId(currentSpec, candidate, proposal),
      );
      return {
        outcome: "healed",
        spec: candidate,
        initial,
        attempts,
        healer: healerInfo(),
      };
    }
    // The post-verification liveness call site: a preview that died DURING the
    // healer call or its replay is the same environment fault as one dead before
    // the attempt — the failed verification measured a corpse, so fail fast as
    // preview-unavailable instead of feeding the "rejection" back as heal signal.
    const postVerification = await livenessCheck(currentSpec.startUrl);
    if (!postVerification.alive) {
      return {
        outcome: "preview-unavailable",
        spec,
        initial,
        attempts,
        healer: healerInfo(),
      };
    }
    priorAttempts.push({
      proposal,
      result: verification.result.failure?.error ?? "failed",
    });
    remember(resolved, verification);
    lastRun = verification;
  }

  return {
    outcome: "unhealed",
    spec,
    initial,
    attempts,
    healer: healerInfo(),
  };
}

/** The audit record says which step a model touched — "healed", not merely "ok". */
function markHealed(result: ReplayResult, stepId: string): void {
  const step = result.steps.find((s) => s.id === stepId);
  if (step) step.outcome = "healed";
}

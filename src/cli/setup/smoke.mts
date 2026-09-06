/**
 * The smoke-heal contract Launchie runs before writing anything (setup.mts): one
 * cheap `propose()` call against the chosen healer, so a bad key or a wrong base URL
 * is caught before a Cocoon is written, not after.
 *
 * `smokeContext()` is a fixed, synthetic failure — one goto, one click on `#approve`
 * that "failed" with "element not found" — whose accessibility snapshot
 * (`- document "Smoke check"`) names no interactive element at all, so `no-repair` is
 * the defensible answer a healthy healer gives.
 *
 * ADJUDICATION: `ok` is a valid, parseable proposal (`parseProposal` accepts it)
 * within the timeout — NOT "the proposal was `no-repair`". The heal rules
 * (heal/brief.mts) push a healer toward proposing SOMETHING rather than declining, so
 * gating success on the exact kind would fail a healthy endpoint that took the rules
 * at their word on a context it cannot fully judge. A non-`no-repair` kind still
 * counts as `ok`; the caller (setup.mts) prints an explicit warning for it.
 *
 * `latencyMs` is wall-clock, measured here — never read off `healer.lastLatencyMs`,
 * which only the OpenAI-compatible healer exposes. `usage` is read from the healer
 * object when present (the same `lastUsage` precedent as openai-compatible.mts), else
 * null — an agent healer has no such field, so usage is always null for one.
 */
import { parseProposal } from "../../heal/proposal.mts";
import type { Spec } from "../../spec/types.mts";
import type { HealContext, Healer } from "../../heal/types.mts";

export interface SmokeResult {
  ok: boolean;
  kind?: string;
  latencyMs: number;
  usage?: { inputTokens: number; outputTokens: number } | null;
  error?: string;
}

export type SmokeHealFn = (
  healer: Healer,
  opts: { timeoutMs?: number },
) => Promise<SmokeResult>;

const SMOKE_URL = "http://127.0.0.1:1/smoke";
const DEFAULT_SMOKE_TIMEOUT_MS = 120_000;

/** A synthetic failure with nothing interactive on the page — pinned exactly so a
 *  reviewer can reason about what "ok" means without re-deriving it. */
export function smokeContext(): HealContext {
  const clickTarget = "#approve";
  const spec: Spec = {
    name: "smoke",
    startUrl: SMOKE_URL,
    steps: [
      { id: "st_1", index: 1, action: "goto", target: SMOKE_URL },
      { id: "st_2", index: 2, action: "click", target: clickTarget },
    ],
  };
  const failedStep = spec.steps[1];
  return {
    spec,
    failure: {
      stepId: failedStep.id,
      index: failedStep.index,
      action: failedStep.action,
      target: clickTarget,
      phase: "action",
      error: "element not found",
    },
    failedStep,
    url: SMOKE_URL,
    ariaSnapshot: '- document "Smoke check"',
    attempt: 1,
    priorAttempts: [],
  };
}

function usageFromHealer(healer: Healer): SmokeResult["usage"] {
  const withUsage = healer as Healer & {
    lastUsage?: { inputTokens: number; outputTokens: number } | null;
  };
  return "lastUsage" in withUsage ? (withUsage.lastUsage ?? null) : null;
}

export const smokeHeal: SmokeHealFn = async (healer, opts) => {
  const timeoutMs = opts.timeoutMs ?? DEFAULT_SMOKE_TIMEOUT_MS;
  const startedAt = Date.now();
  let timer: ReturnType<typeof setTimeout>;
  const timedOut = new Promise<never>((_, reject) => {
    timer = setTimeout(
      () => reject(new Error(`timed out after ${timeoutMs} ms`)),
      timeoutMs,
    );
  });
  try {
    const raw = await Promise.race([healer.propose(smokeContext()), timedOut]);
    const proposal = parseProposal(raw);
    return {
      ok: true,
      kind: proposal.kind,
      latencyMs: Date.now() - startedAt,
      usage: usageFromHealer(healer),
    };
  } catch (error) {
    return {
      ok: false,
      latencyMs: Date.now() - startedAt,
      error: (error as Error).message,
    };
  } finally {
    clearTimeout(timer!);
  }
};

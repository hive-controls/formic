/**
 * Healer selection by environment — the user's choice of model fit and cost per run.
 *
 *   FORMIC_HEALER            openai-compatible (default) | agent:claude | agent:codex |
 *                           agent:kimi | agent:grok | agent:custom
 *   FORMIC_HEALER_BASE_URL   OpenAI-compatible base, e.g. https://openrouter.ai/api/v1,
 *                           http://qwen-box:11434/v1. Default: Anthropic's compatible
 *                           endpoint (⚠️ ASSUMED https://api.anthropic.com/v1 — verified
 *                           by the first live run, not by this file).
 *   FORMIC_HEALER_MODEL      default claude-sonnet-5 (user decision, 2026-09-01)
 *   FORMIC_HEALER_API_KEY    bearer for the endpoint; optional for local servers
 *   FORMIC_HEALER_AGENT_CMD  for agent:custom — "mycli --flag {prompt}"
 *   FORMIC_HEALER_TIMEOUT_MS per proposal
 */
import type { Healer } from "../types.mts";
import { adapterFromCommand, ADAPTERS, agentCliHealer } from "./agent-cli.mts";
import { openAiCompatibleHealer } from "./openai-compatible.mts";

export const DEFAULT_BASE_URL = "https://api.anthropic.com/v1";
export const DEFAULT_MODEL = "claude-sonnet-5";

export function healerFromEnv(env: NodeJS.ProcessEnv = process.env): Healer {
  const choice = env.FORMIC_HEALER || "openai-compatible";
  const timeoutMs = env.FORMIC_HEALER_TIMEOUT_MS
    ? Number(env.FORMIC_HEALER_TIMEOUT_MS)
    : undefined;

  if (choice === "openai-compatible") {
    return openAiCompatibleHealer({
      baseUrl: env.FORMIC_HEALER_BASE_URL || DEFAULT_BASE_URL,
      model: env.FORMIC_HEALER_MODEL || DEFAULT_MODEL,
      apiKey: env.FORMIC_HEALER_API_KEY || undefined,
      timeoutMs,
    });
  }
  if (choice.startsWith("agent:")) {
    const name = choice.slice("agent:".length);
    const adapter =
      name === "custom"
        ? adapterFromCommand(env.FORMIC_HEALER_AGENT_CMD ?? "")
        : ADAPTERS[name];
    if (!adapter) {
      throw new Error(
        `unsupported FORMIC_HEALER "${choice}" (expected agent:${Object.keys(ADAPTERS).join(" | agent:")} | agent:custom)`,
      );
    }
    return agentCliHealer({
      adapter,
      workspaceRoot: env.FORMIC_HEAL_WORKSPACES || undefined,
      timeoutMs,
      env,
    });
  }
  throw new Error(
    `unsupported FORMIC_HEALER "${choice}" (expected openai-compatible or agent:<name>)`,
  );
}

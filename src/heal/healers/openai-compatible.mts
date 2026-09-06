/**
 * Healer backed by any OpenAI-compatible chat completions endpoint.
 *
 * Provider-neutral by design (user decision, 2026-09-01): one backend over native
 * fetch covers OpenRouter, OpenAI, Ollama/LM Studio/vLLM on a LAN, and Anthropic via
 * its OpenAI-compatible endpoint, with the model chosen per run by env. No SDK.
 *
 * The response is data: the first JSON object in the reply is parsed strictly by
 * parseProposal. Anything else — prose, a refusal, a malformed object — is an error
 * the loop reports, never something it guesses around.
 *
 * `lastUsage`/`lastLatencyMs` ride on the returned healer object, never on the
 * RepairProposal itself — the proposal is validated strictly (proposal.mts) and any
 * extra field is refused, so usage/cost telemetry needs its own home (the same
 * precedent as `lastWorkspace` on `agentCliHealer`, agent-cli.mts).
 */
import { buildBrief, extractJsonObject, HEALER_RULES } from "../brief.mts";
import { parseProposal } from "../proposal.mts";
import type { HealContext, Healer, RepairProposal } from "../types.mts";

export interface OpenAiCompatibleOptions {
  /** e.g. https://openrouter.ai/api/v1, http://qwen-box:11434/v1 — no trailing slash needed. */
  baseUrl: string;
  model: string;
  apiKey?: string;
  timeoutMs?: number;
  /** Test seam. */
  fetchImpl?: typeof fetch;
}

/** Token counts from the response — both the OpenAI (`prompt_/completion_`) and the
 *  input_/output_ naming a provider may use are accepted. */
export interface HealerUsage {
  inputTokens: number;
  outputTokens: number;
}

interface ChatCompletion {
  choices?: { message?: { content?: string | null } }[];
  error?: { message?: string };
  usage?: {
    prompt_tokens?: number;
    completion_tokens?: number;
    input_tokens?: number;
    output_tokens?: number;
  };
}

function usageFrom(usage: ChatCompletion["usage"]): HealerUsage | null {
  const inputTokens = usage?.prompt_tokens ?? usage?.input_tokens;
  const outputTokens = usage?.completion_tokens ?? usage?.output_tokens;
  if (typeof inputTokens !== "number" || typeof outputTokens !== "number")
    return null;
  return { inputTokens, outputTokens };
}

export function openAiCompatibleHealer(
  options: OpenAiCompatibleOptions,
): Healer & { lastUsage: HealerUsage | null; lastLatencyMs: number | null } {
  const fetchImpl = options.fetchImpl ?? fetch;
  const endpoint = `${options.baseUrl.replace(/\/+$/, "")}/chat/completions`;

  const healer = {
    name: "openai-compatible",
    modelVersion: options.model,
    lastUsage: null as HealerUsage | null,
    lastLatencyMs: null as number | null,
    async propose(context: HealContext): Promise<RepairProposal> {
      const headers: Record<string, string> = {
        "content-type": "application/json",
      };
      if (options.apiKey) headers.authorization = `Bearer ${options.apiKey}`;

      const startedAt = Date.now();
      const response = await fetchImpl(endpoint, {
        method: "POST",
        headers,
        signal: AbortSignal.timeout(options.timeoutMs ?? 120_000),
        body: JSON.stringify({
          model: options.model,
          temperature: 0,
          messages: [
            { role: "system", content: HEALER_RULES },
            { role: "user", content: buildBrief(context) },
          ],
        }),
      });
      const text = await response.text();
      healer.lastLatencyMs = Date.now() - startedAt;
      if (!response.ok) {
        throw new Error(
          `${options.model} at ${endpoint}: HTTP ${response.status} — ${text.slice(0, 300)}`,
        );
      }
      const completion = JSON.parse(text) as ChatCompletion;
      healer.lastUsage = usageFrom(completion.usage);
      const content = completion.choices?.[0]?.message?.content;
      if (typeof content !== "string" || content.trim() === "") {
        throw new Error(
          `${options.model} returned no content${completion.error?.message ? `: ${completion.error.message}` : ""}`,
        );
      }
      return parseProposal(extractJsonObject(content));
    },
  };
  return healer;
}

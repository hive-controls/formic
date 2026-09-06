/**
 * Healer backed by a headless terminal agent — Claude Code, Codex, Kimi, Grok, or any
 * CLI the user names — running on the user's own subscription.
 *
 * The agent is spawned inside a materialized heal workspace (workspace.mts) with a
 * fixed prompt and a tool allowlist limited to that directory. It reads HEAL.md and
 * writes PROPOSAL.yaml; the harness reads nothing else back. Verification is the
 * same replay every other healer gets.
 *
 * VERIFICATION STATUS of each adapter is stated on the adapter, per the repo's rule
 * that a shipped artifact may not assert a capability nobody has exercised:
 * `verified: true` means a live heal ran through it on a real breakage.
 */
import { spawn } from "node:child_process";
import { mkdtempSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";
import { parseProposal } from "../proposal.mts";
import type { HealContext, Healer, RepairProposal } from "../types.mts";
import { materializeWorkspace, readProposalRaw } from "../workspace.mts";

export interface AgentCliAdapter {
  name: string;
  command: string;
  /** argv for a headless run; `{prompt}` tokens are replaced by the prompt. */
  args: string[];
  versionArgs: string[];
  /** A live heal has run through this adapter on a real breakage. */
  verified: boolean;
  /** Extra argv appended when a profile names a model, e.g. `(m) => ["--model", m]`.
   *  Only this or modelEnv may carry a model — never both, never a session-start hook
   *  (out-of-band, invisible to the harness — the printed label would confidently lie). */
  modelArgs?: (model: string) => string[];
  /** Env var name the model is passed through, alternative to modelArgs. */
  modelEnv?: string;
}

/** The prompt is identical for every agent; the .md files carry the detail. */
export const AGENT_PROMPT =
  "Read HEAL.md in the current directory and write your repair proposal to PROPOSAL.yaml in exactly the YAML shape it describes, including a `reason`. Do not modify any other file. Then stop.";

export const ADAPTERS: Record<string, AgentCliAdapter> = {
  claude: {
    name: "claude",
    command: "claude",
    args: [
      "-p",
      "{prompt}",
      "--allowedTools",
      "Read",
      "Write",
      "Edit",
      "Glob",
      "Grep",
      "--permission-mode",
      "acceptEdits",
      "--output-format",
      "text",
    ],
    versionArgs: ["--version"],
    // VERIFIED live 2026-09-01, Claude Code 2.1.257: breakage class 1 (renamed selector) healed (role
    // locator, 53 s), class 3 healed (inserted confirming click, 68 s), class 4
    // correctly needs-human (assertion proposal, 42 s).
    verified: true,
    // ✅ the --model flag exists; ❓ not exercised by a live heal in this repo.
    modelArgs: (model: string) => ["--model", model],
  },
  codex: {
    name: "codex",
    command: "codex",
    // A heal workspace is not a git repo; without the skip flag codex refuses it
    // ("Not inside a trusted directory") — measured on codex-cli 0.151.0.
    args: [
      "exec",
      "--skip-git-repo-check",
      "--sandbox",
      "workspace-write",
      "{prompt}",
    ],
    versionArgs: ["--version"],
    // VERIFIED live 2026-09-01, codex-cli 0.151.0: class 1 healed (71 s). On codex-cli
    // 0.152.1 (2026-09-02, one observation) the setup smoke heal failed once with a
    // proposal missing `kind` — not re-verified on that version. Wrote the
    // kind-as-key shape without `reason` on both runs — see withReasonFromOutput.
    verified: true,
    // ⚠️ ASSUMED — `codex exec --model <m>`, not exercised by a live heal.
    modelArgs: (model: string) => ["--model", model],
  },
  kimi: {
    name: "kimi",
    command: "kimi",
    // kimi 0.39.1 refuses `--prompt` together with `--yolo` AND with `--auto` (both
    // measured); prompt mode runs with its own permission defaults.
    args: ["-p", "{prompt}"],
    versionArgs: ["--version"],
    // VERIFIED live 2026-09-01, kimi 0.39.1: class 1 healed (32 s).
    verified: true,
  },
  grok: {
    name: "grok",
    command: "grok",
    args: ["-p", "{prompt}"],
    versionArgs: ["--version"],
    // VERIFIED live 2026-09-01, grok 1.0.13: class 1 healed (83 s).
    verified: true,
  },
};

/** Splits a command template into tokens on whitespace, honoring quotes. Purely
 *  syntactic — it never touches the filesystem, so the same template resolves to the
 *  same argv on every machine and in every working directory.
 *
 *  Single quotes are literal: there are no escapes inside them at all. Inside double
 *  quotes a backslash is ALSO literal, except immediately before a double quote,
 *  where `\"` yields one `"`. That one rule is what lets a Windows path survive
 *  unmangled — `"C:\Program Files\nodejs\node.exe"` and the UNC form
 *  `"\\server\share\node.exe"` both round-trip with their backslashes intact, with no
 *  doubling for the user to remember.
 *
 *  Throws on an unbalanced quote rather than silently closing it at end of input: a
 *  misquoted command is a configuration mistake, and guessing at it would spawn
 *  something the user did not write. The message names the error kind and the
 *  character offset of the quote that was never closed — never the template itself,
 *  which can legitimately carry an inline credential and is printed by the CLI. */
function splitCommandTokens(template: string): string[] {
  const tokens: string[] = [];
  let value = "";
  let started = false;
  let inSingle = false;
  let inDouble = false;
  let quoteOpenedAt = -1;
  for (let i = 0; i < template.length; i++) {
    const char = template[i];
    if (inSingle) {
      if (char === "'") inSingle = false;
      else value += char;
      continue;
    }
    if (inDouble) {
      if (char === '"') inDouble = false;
      else if (char === "\\" && template[i + 1] === '"') value += template[++i];
      else value += char;
      continue;
    }
    if (char === "'" || char === '"') {
      if (char === "'") inSingle = true;
      else inDouble = true;
      quoteOpenedAt = i;
      started = true;
    } else if (/\s/.test(char)) {
      if (started) tokens.push(value);
      value = "";
      started = false;
    } else {
      value += char;
      started = true;
    }
  }
  if (inSingle || inDouble) {
    throw new Error(
      `FORMIC_HEALER_AGENT_CMD has an unbalanced ${inSingle ? "single" : "double"} quote opened at character ${quoteOpenedAt}`,
    );
  }
  if (started) tokens.push(value);
  return tokens;
}

/** Wraps one token so splitCommandTokens gives it back unchanged — the form every
 *  writer of a healer command must use for a path that may contain whitespace.
 *
 *  The form is chosen so the tokenizer can actually read it back. Double quotes are
 *  the default (`\"` escapes an embedded quote, backslashes stay literal), but a
 *  token ending in a backslash cannot use them at all: the closing quote would be
 *  read as an escaped `\"`. Such a token — and any token carrying a double quote —
 *  goes in single quotes, which have no escapes and so need none. A token that both
 *  ends in a backslash and contains a single quote is expressible in neither form,
 *  and is refused by name rather than emitted broken. */
export function quoteCommandToken(value: string): string {
  const endsWithBackslash = value.endsWith("\\");
  const preferSingle = endsWithBackslash || value.includes('"');
  if (preferSingle && !value.includes("'")) return `'${value}'`;
  if (endsWithBackslash) {
    throw new Error(
      "a command token ending in a backslash and containing a single quote cannot be quoted for FORMIC_HEALER_AGENT_CMD",
    );
  }
  return `"${value.replace(/"/g, '\\"')}"`;
}

/** `FORMIC_HEALER_AGENT_CMD="mycli --headless {prompt}"` → an adapter, no code change.
 *  A `{model}` token (e.g. `mycli {prompt} --model {model}`) opts the custom command
 *  into a model, substituted in place by buildArgs; its presence here is also the
 *  capability marker setup.mts/resolve.mts check to know a model can be carried.
 *  An executable path containing whitespace MUST be quoted (see quoteCommandToken):
 *  whitespace is the token boundary, and nothing here probes the filesystem to guess
 *  where an unquoted path ends. */
export function adapterFromCommand(template: string): AgentCliAdapter {
  const [command, ...args] = splitCommandTokens(template.trim());
  // Trimmed: a quoted whitespace-only executable ("   ") is as empty as no token at all.
  if (!command?.trim()) {
    throw new Error(
      "FORMIC_HEALER_AGENT_CMD is empty: it must name a command, and an executable path containing whitespace must be quoted",
    );
  }
  return {
    name: basename(command),
    command,
    args,
    // Best effort: the template's fixed args with `--version` in place of the prompt;
    // a `{model}` token is kept (not stripped) so the version check never runs with a
    // flag missing its value — substituted at call time, same as `args`.
    versionArgs: [...args.filter((a) => a !== "{prompt}"), "--version"],
    verified: false,
    ...(args.includes("{model}")
      ? { modelArgs: (model: string) => [model] }
      : {}),
  };
}

/** A `{model}`-bearing template requires a model — an unfilled token would leave a
 *  dangling flag (`--model` with no value) on every spawn, including the version
 *  check. Construction-time, before anything can be spawned with one missing. */
function requireModelForToken(
  adapter: AgentCliAdapter,
  model: string | undefined,
): void {
  if (model || !adapter.args.includes("{model}")) return;
  throw new Error(
    `${adapter.name}: command "${[adapter.command, ...adapter.args].join(" ")}" names {model} but no model was set — add model: <name> to the healer profile`,
  );
}

/** Substitutes a `{model}` token in place wherever it occurs; guaranteed present by
 *  the time this runs (requireModelForToken refuses construction otherwise). */
function substituteModelToken(
  argv: string[],
  model: string | undefined,
): string[] {
  return argv.includes("{model}")
    ? argv.map((a) => (a === "{model}" ? model! : a))
    : argv;
}

/** `{prompt}` is always substituted; a `{model}` token is substituted in place when
 *  the template names one, otherwise (a named adapter's own modelArgs) the model — if
 *  any — is appended. */
function buildArgs(
  adapter: AgentCliAdapter,
  model: string | undefined,
): string[] {
  const withPrompt = substituteModelToken(
    adapter.args.map((a) => (a === "{prompt}" ? AGENT_PROMPT : a)),
    model,
  );
  return model && adapter.modelArgs && !adapter.args.includes("{model}")
    ? [...withPrompt, ...adapter.modelArgs(model)]
    : withPrompt;
}

export interface AgentCliOptions {
  adapter: AgentCliAdapter;
  /** Workspaces are created under here and KEPT after the run — they are audit material. */
  workspaceRoot?: string;
  timeoutMs?: number;
  env?: NodeJS.ProcessEnv;
  /** From the profile; only passed on when the adapter declares a route. */
  model?: string;
}

interface Spawned {
  code: number | null;
  stdout: string;
  stderr: string;
  timedOut: boolean;
}

function run(
  command: string,
  args: string[],
  cwd: string,
  env: NodeJS.ProcessEnv,
  timeoutMs: number,
): Promise<Spawned> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd,
      env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGKILL");
    }, timeoutMs);
    child.stdout.on("data", (c: Buffer) => (stdout += c.toString()));
    child.stderr.on("data", (c: Buffer) => (stderr += c.toString()));
    child.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.on("close", (code) => {
      clearTimeout(timer);
      resolve({ code, stdout, stderr, timedOut });
    });
  });
}

/** Whether `model` can actually reach `adapter` (either route) — the single check
 *  both `agentCliHealer` and the label (resolve.mts's modelPassed reads) rely on. */
function modelCanBePassed(
  adapter: AgentCliAdapter,
  model: string | undefined,
): boolean {
  return Boolean(model) && Boolean(adapter.modelArgs || adapter.modelEnv);
}

/** A nested Claude Code refuses to start inside another; the marker is per-session and
 *  must not leak into the child. A modelEnv adapter gets the model folded in here. */
function buildEnv(
  base: NodeJS.ProcessEnv | undefined,
  adapter: AgentCliAdapter,
  model: string | undefined,
  modelPassed: boolean,
): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = { ...(base ?? process.env) };
  delete env.CLAUDECODE;
  if (modelPassed && adapter.modelEnv) env[adapter.modelEnv] = model!;
  return env;
}

export function agentCliHealer(
  options: AgentCliOptions,
): Healer & { lastWorkspace: string | null } {
  const { adapter, model } = options;
  requireModelForToken(adapter, model);
  const timeoutMs = options.timeoutMs ?? 180_000;
  const modelPassed = modelCanBePassed(adapter, model);
  const env = buildEnv(options.env, adapter, model, modelPassed);

  let version: string | undefined;
  const healer = {
    name: `agent:${adapter.name}`,
    // Resolved model when one was named AND actually passed; otherwise the CLI's own
    // build — the audit field must never claim a model was used when the harness did
    // not actually pass it to the adapter.
    get modelVersion() {
      return modelPassed
        ? model!
        : `agent:${adapter.name}@${version ?? "unknown"}`;
    },
    modelRequested: model ?? null,
    modelPassed,
    lastWorkspace: null as string | null,
    async propose(context: HealContext): Promise<RepairProposal> {
      if (version === undefined) {
        const v = await run(
          adapter.command,
          substituteModelToken(adapter.versionArgs, model),
          process.cwd(),
          env,
          30_000,
        ).catch(() => null);
        version = v?.stdout.trim().split("\n")[0] || "unknown";
      }
      const dir = mkdtempSync(
        join(
          options.workspaceRoot ?? tmpdir(),
          `e2e-doctor-heal-${adapter.name}-`,
        ),
      );
      healer.lastWorkspace = dir;
      const workspace = materializeWorkspace(context, dir);
      const args = buildArgs(adapter, modelPassed ? model : undefined);
      const outcome = await run(adapter.command, args, dir, env, timeoutMs);
      if (outcome.timedOut) {
        throw new Error(
          `${adapter.name} did not finish within ${timeoutMs} ms (workspace: ${dir})`,
        );
      }
      if (outcome.code !== 0) {
        throw new Error(
          `${adapter.name} exited ${outcome.code}: ${(outcome.stderr || outcome.stdout).trim().slice(0, 400)} (workspace: ${dir})`,
        );
      }
      return parseProposal(
        withReasonFromOutput(readProposalRaw(workspace), outcome.stdout),
      );
    },
  };
  return healer;
}

/**
 * Agents put their reasoning on stdout and forget the `reason` field (codex-cli
 * 0.151.0 did, twice, on live runs). The reviewer still needs a why, so the agent's
 * own final output stands in — marked as such, so nobody mistakes it for a field the
 * agent wrote deliberately. A proposal with no reason AND no output stays invalid.
 */
export function withReasonFromOutput(raw: unknown, stdout: string): unknown {
  if (typeof raw !== "object" || raw === null || Array.isArray(raw)) return raw;
  const record = raw as Record<string, unknown>;
  const nested = Object.values(record).find(
    (v) => typeof v === "object" && v !== null && !Array.isArray(v),
  ) as Record<string, unknown> | undefined;
  const hasReason =
    typeof record.reason === "string" || typeof nested?.reason === "string";
  const output = stdout.trim();
  if (hasReason || output === "") return raw;
  return { ...record, reason: `[from agent output] ${output.slice(-600)}` };
}

/**
 * The named agent-CLI presets the configurator can resolve — argv, as DATA.
 *
 * Formic never spawns a healer. A profile that names `kimi` resolves to two
 * environment variables the recipe reads (`…_HEALER=agent:custom` and
 * `…_HEALER_AGENT_CMD=<argv>`), so the argv that earned each `verified` line lives
 * here and nothing else about the healer does. Each entry's verification note is the
 * live run that earned it, copied verbatim from where it was measured.
 *
 * `claude` is in this table too: the recipe ships it as a named adapter of its own,
 * but a configurator that could only name three of the four would be a worse contract
 * than one that can name all of them through the generic custom-command route.
 */

export interface AgentPreset {
  name: string;
  command: string;
  /** argv for a headless run; a `{prompt}` token is filled in by the recipe. */
  args: string[];
  /** A live heal has run through this argv on a real breakage. */
  verified: boolean;
  /** Extra argv a model name is appended as, when the CLI exposes a selector. */
  modelArgs?: (model: string) => string[];
}

export const AGENT_PRESETS: Record<string, AgentPreset> = {
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
    // VERIFIED live 2026-09-01, Claude Code 2.1.257: breakage class 1 (renamed
    // selector) healed (role locator, 53 s), class 3 healed (inserted confirming
    // click, 68 s), class 4 correctly needs-human (assertion proposal, 42 s).
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
    // VERIFIED live 2026-09-01, codex-cli 0.151.0: class 1 healed (71 s). On codex-cli
    // 0.152.1 (2026-09-02, one observation) the setup smoke heal failed once with a
    // proposal missing `kind` — not re-verified on that version.
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
    // VERIFIED live 2026-09-01, kimi 0.39.1: class 1 healed (32 s).
    verified: true,
  },
  grok: {
    name: "grok",
    command: "grok",
    args: ["-p", "{prompt}"],
    // VERIFIED live 2026-09-01, grok 1.0.13: class 1 healed (83 s).
    verified: true,
  },
};

/**
 * Wrap one token so the recipe's own command-template tokenizer gives it back
 * unchanged. Double quotes are the default (`\"` escapes an embedded quote,
 * backslashes stay literal), but a token ending in a backslash cannot use them: the
 * closing quote would read as an escaped `\"`. Such a token — and any token carrying a
 * double quote — goes in single quotes, which have no escapes and so need none. A
 * token that both ends in a backslash and contains a single quote is expressible in
 * neither form, and is refused by name rather than emitted broken. Mirrors the
 * recipe's own `quoteCommandToken`; the round trip is what the contract is.
 */
export function quoteCommandToken(value: string): string {
  const endsWithBackslash = value.endsWith("\\");
  const preferSingle = endsWithBackslash || value.includes('"');
  if (preferSingle && !value.includes("'")) return `'${value}'`;
  if (endsWithBackslash) {
    throw new Error(
      `a command token ending in a backslash and containing a single quote cannot be quoted for a healer command`,
    );
  }
  return `"${value.replace(/"/g, '\\"')}"`;
}

/** Only a token that needs quoting gets quoted — an unquoted argv is what a human
 *  reading the resolved line expects to see, and `{prompt}` must survive verbatim. */
function maybeQuote(token: string): string {
  return /^[A-Za-z0-9_@%+=:,./{}-]+$/.test(token)
    ? token
    : quoteCommandToken(token);
}

/**
 * The command template a preset resolves to. A model is baked in HERE, already
 * substituted, rather than left to a `…_HEALER_MODEL` the recipe's generic
 * custom-command adapter exposes no route for: the printed label and the audit row
 * can only be true to argv that was actually passed.
 */
export function presetCommandTemplate(
  preset: AgentPreset,
  model?: string,
): string {
  const argv = [preset.command, ...preset.args];
  if (model && preset.modelArgs) argv.push(...preset.modelArgs(model));
  return argv.map(maybeQuote).join(" ");
}

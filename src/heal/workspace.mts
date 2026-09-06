/**
 * The heal workspace: a directory an agent can be pointed at.
 *
 * The harness owns everything in it; the agent owns exactly one file, PROPOSAL.yaml.
 * That boundary is what makes a headless coding agent as safe a healer as a model
 * behind an API: whatever it does inside the directory, the only thing the harness
 * reads back is a proposal, parsed strictly. The spec and the repo are never exposed.
 *
 * The same brief is written under each agent runtime's instruction-file convention
 * (CLAUDE.md for Claude Code, AGENTS.md for Codex and others), so the "simple .md — export-denylist: ok
 * files" surface is one brief under several names.
 */
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { parse as parseYaml } from "yaml";
import { saveSpec } from "../spec/parse.mts";
import { buildBrief, HEALER_RULES } from "./brief.mts";
import { parseProposal } from "./proposal.mts";
import type { HealContext, RepairProposal } from "./types.mts";

export const PROPOSAL_FILE = "PROPOSAL.yaml";

const PROPOSAL_TEMPLATE = `# Write your repair proposal here, replacing this file's contents.
# Exactly ONE of the shapes below, as TOP-LEVEL keys (\`kind:\` first). Leave no other keys.
# \`reason\` is REQUIRED on every proposal — it is what the reviewer reads.
#
# kind: rewrite-target
# stepId: st_xxxxxxxx
# target: "#new-locator"
# reason: why
#
# kind: insert-step
# beforeStepId: st_xxxxxxxx
# step:
#   action: click
#   target: "#locator"
#   assert:
#     selector: "#what-the-user-sees-after"
#     visible: true
# reason: why
#
# kind: propose-assert-change
# stepId: st_xxxxxxxx
# to:
#   testId: some-id
#   hasText: expected text
# reason: why
#
# kind: no-repair
# reason: why
`;

const POINTER = `# Heal task

Read \`HEAL.md\` in this directory. Write your repair proposal to \`${PROPOSAL_FILE}\`
in exactly the YAML shape it describes, then stop.

Do not modify any other file. Do not run the application. Do not search outside this
directory. The harness verifies your proposal by replaying the whole test itself.
`;

export interface HealWorkspace {
  dir: string;
  proposalFile: string;
}

export function materializeWorkspace(
  context: HealContext,
  dir: string,
): HealWorkspace {
  mkdirSync(join(dir, "context"), { recursive: true });
  writeFileSync(
    join(dir, "HEAL.md"),
    `${HEALER_RULES.replace(/Reply with ONLY a JSON object[\s\S]*$/, "")}
Write your proposal to \`${PROPOSAL_FILE}\` as YAML (the file shows the shapes). Then stop.

${buildBrief(context)}`,
  );
  writeFileSync(join(dir, "CLAUDE.md"), POINTER);
  writeFileSync(join(dir, "AGENTS.md"), POINTER); // export-denylist: ok
  writeFileSync(join(dir, "context", "spec.yaml"), saveSpec(context.spec));
  writeFileSync(
    join(dir, "context", "failure.json"),
    JSON.stringify(context.failure, null, 2),
  );
  writeFileSync(
    join(dir, "context", "aria-snapshot.txt"),
    context.ariaSnapshot,
  );
  const proposalFile = join(dir, PROPOSAL_FILE);
  writeFileSync(proposalFile, PROPOSAL_TEMPLATE);
  return { dir, proposalFile };
}

/** The raw YAML value the agent wrote, or an error when it wrote nothing. */
export function readProposalRaw(workspace: HealWorkspace): unknown {
  const text = readFileSync(workspace.proposalFile, "utf8");
  const parsed: unknown = parseYaml(text);
  if (parsed === null || parsed === undefined) {
    throw new Error(
      `${PROPOSAL_FILE} was not written — the agent produced no proposal`,
    );
  }
  return parsed;
}

/** Strict: an untouched template, an empty file, or a stray key is a failure to
 *  propose — reported as such, never coerced into a proposal. */
export function readProposal(workspace: HealWorkspace): RepairProposal {
  return parseProposal(readProposalRaw(workspace));
}

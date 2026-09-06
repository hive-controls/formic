/**
 * The repair PR — the product surface a human actually reviews.
 *
 * The PR carries: the outcome and who acted; the step-keyed spec diff; the BEFORE and
 * AFTER frames inline (GitHub renders images from the branch); a link to the
 * self-contained evidence page; the audit fields; and, for needs-human, the assertion
 * proposal with what accepting or rejecting it means. Everything in the body is a
 * deterministic function of the heal result — the reviewer reads what happened, not a
 * model's account of it.
 *
 * Git and gh run in a throwaway worktree so the user's checkout is never touched.
 * Commands go through an injectable runner so the whole flow is testable offline.
 */
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, posix } from "node:path";
import { saveSpec } from "../spec/parse.mts";
import type { Spec } from "../spec/types.mts";
import { missingShippedFrameNote, PRODUCT_NAME } from "../evidence/page.mts";
import type { EvidenceBundle } from "./bundle.mts";
import type { UiDrift, UntestedComponent } from "../evidence/coverage.mts";
import type { HealResult } from "./loop.mts";

export interface CommandRunner {
  (
    command: string,
    args: string[],
    options: { cwd: string },
  ): Promise<{ stdout: string }>;
}

export interface RepairPrInput {
  repoRoot: string;
  /** Repo-relative path of the spec file. */
  specFile: string;
  /** Repo-relative directory the bundle is committed to. */
  bundleDir: string;
  original: Spec;
  result: HealResult;
  bundle: EvidenceBundle;
  /** Branch to target; default: the current branch. */
  base?: string;
  remote?: string;
  run: CommandRunner;
  /** Copies the bundle's files into the worktree; injectable for tests. */
  copyBundle?: (bundle: EvidenceBundle, targetDir: string) => void;
}

export interface RepairPr {
  branch: string;
  url: string;
  title: string;
  body: string;
}

function stepFrameMarkdown(
  index: number,
  shipped: ReadonlySet<string>,
  rawBase: string,
  bundleDir: string,
): string {
  const name = `step-${index}`;
  if (shipped.has(name)) {
    return `[step ${index}](${rawBase}/${bundleDir}/frames/${name}.png?raw=true)`;
  }
  return missingShippedFrameNote(index);
}

function slug(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-|-$/g, "");
}

export function prTitle(result: HealResult): string {
  const what =
    result.outcome === "healed"
      ? `heal ${result.spec.name}`
      : result.outcome === "needs-human"
        ? `${result.spec.name} needs a human decision`
        : `${result.spec.name}: ${result.outcome}`;
  return `${PRODUCT_NAME}: ${what}`;
}

/** Playwright colours its assertion errors. A terminal renders those escapes;
 *  a markdown code block prints them as literal garbage, so strip them before
 *  the failure text becomes part of a public pull request. */
function withoutTerminalEscapes(text: string): string {
  // CSI sequences: ESC [ ... final-byte. Covers SGR colour/reset, which is all
  // a runner error carries.
  return text.replace(/\u001b\[[0-9;]*[A-Za-z]/g, "");
}

export function prBody(input: {
  result: HealResult;
  diff: string[];
  bundleDir: string;
  rawBase: string;
  frames: string[];
  /** Untested components from the green run (bundle.untested). */
  untested?: UntestedComponent[];
  /** UI drift since the previous record (bundle.drift). */
  drift?: UiDrift | null;
}): string {
  const { result, diff, bundleDir, rawBase, frames } = input;
  const untested = input.untested ?? [];
  const drift = input.drift ?? null;
  const shipped = new Set(frames);
  const initial = result.initial;
  const lines: string[] = [];
  const healerLine =
    result.attempts.length > 0
      ? `Healer: \`${result.healer.name}\` (${result.healer.modelVersion}).`
      : "No healer was consulted.";

  lines.push(`**Outcome: ${result.outcome}.** ${healerLine}`);
  if (initial.result.failure) {
    lines.push(
      "",
      `The replay failed at step ${initial.result.failure.index} (${initial.result.failure.phase} phase):`,
      "",
      "```",
      withoutTerminalEscapes(
        initial.result.failure.error.split("\n").slice(0, 6).join("\n"),
      ),
      "```",
    );
  }

  if (diff.length > 0) {
    lines.push(
      "",
      "## What changed in the spec",
      "",
      "```diff",
      ...diff,
      "```",
    );
  }

  for (const attempt of result.attempts) {
    const verdict = attempt.verification
      ? attempt.verification.result.outcome === "passed"
        ? "verified by a full green replay"
        : "rejected — the replay still failed"
      : attempt.proposal.kind === "propose-assert-change"
        ? "not applied — awaiting your decision"
        : "declined";
    lines.push(
      "",
      `## Attempt ${attempt.attempt}: ${attempt.proposal.kind} — ${verdict}`,
      "",
      `> ${attempt.proposal.reason.replace(/\n/g, "\n> ")}`,
    );
    const before = frames.find(
      (f) => f === `attempt-${attempt.attempt}-before`,
    );
    const after = frames.find((f) => f === `attempt-${attempt.attempt}-after`);
    if (before || after) {
      lines.push(
        "",
        "| BEFORE — the step as it failed | AFTER — the repaired step |",
        "| --- | --- |",
      );
      lines.push(
        `| ${before ? `![before](${rawBase}/${bundleDir}/frames/${before}.png?raw=true)` : "—"} | ${after ? `![after](${rawBase}/${bundleDir}/frames/${after}.png?raw=true)` : "—"} |`,
      );
    }
    if (attempt.proposal.kind === "propose-assert-change") {
      lines.push(
        "",
        "The locator works and the click landed; what is on screen differs from what the spec expects. This is either a stale expectation or a real bug, and only you can tell which:",
        "",
        `- **Accept** — the data legitimately changed: move \`proposedAssertChange.to\` into \`assert\` and delete the proposal.`,
        `- **Reject** — the application is wrong: close this PR and file the bug; the spec stays as it is and keeps failing, which is correct.`,
      );
    }
  }

  if (drift && drift.steps.length > 0) {
    const names = (nodes: { role: string; name: string }[]) =>
      nodes
        .map((n) => `\`${n.role}\` ${n.name.replace(/\|/g, "\\|")}`)
        .join(", ") || "—";
    lines.push(
      "",
      `## UI drift since \`${drift.previousDecisionId}\` (${drift.previousTimestamp})`,
      "",
      "Interactive elements that appeared or disappeared in a step's end state since the previous run of this spec (from the accessibility snapshots; no model involved). Worth a look even though the replay is green.",
      "",
      "| step | appeared | disappeared |",
      "| --- | --- | --- |",
      ...drift.steps.map(
        (d) =>
          `| ${stepFrameMarkdown(d.index, shipped, rawBase, bundleDir)} | ${names(d.added)} | ${names(d.removed)} |`,
      ),
    );
  }

  if (untested.length > 0) {
    const shown = untested.slice(0, 10);
    lines.push(
      "",
      `## Untested components — ${untested.length} proposed step(s), not applied`,
      "",
      "Interactive elements the app showed during the green run that no spec step acts on (from the accessibility snapshots; no model involved). Each links to the frame that shows it; \`proposals.json\` in the evidence directory carries the step YAML to paste.",
      "",
      "| component | first seen | proposed step |",
      "| --- | --- | --- |",
      ...shown.map(
        (c) =>
          `| \`${c.role}\` ${c.name.replace(/\|/g, "\\|")} | ${stepFrameMarkdown(c.firstSeenIndex, shipped, rawBase, bundleDir)} | \`${c.suggestedAction}\` \`${c.suggestedTarget.replace(/\|/g, "\\|")}\` |`,
      ),
    );
    if (untested.length > shown.length)
      lines.push(
        "",
        `…and ${untested.length - shown.length} more in \`proposals.json\`.`,
      );
  }

  lines.push(
    "",
    "## Evidence",
    "",
    `Full replay segments per step, playable offline: [\`${bundleDir}/index.html\`](${rawBase}/${bundleDir}/index.html) (download the file from the branch and open it; GitHub shows its source).`,
    "",
    "| field | value |",
    "| --- | --- |",
    `| decision id | \`${initial.evidence.decisionId}\` |`,
    `| timestamp | ${initial.evidence.timestamp} |`,
    `| system version | ${initial.evidence.systemVersion} |`,
    `| model version | ${result.attempts.length > 0 ? result.healer.modelVersion : "none (token-free run)"} |`,
    `| driver / session | ${initial.evidence.driver} / \`${initial.evidence.sessionId ?? "none"}\` |`,
    "",
    `Opened by ${PRODUCT_NAME}. Nothing in this description was written by a model; the quoted reason is the healer's own, verbatim.`,
  );
  return lines.join("\n");
}

function defaultCopyBundle(bundle: EvidenceBundle, targetDir: string): void {
  mkdirSync(targetDir, { recursive: true });
  for (const file of bundle.files) {
    const target = join(targetDir, file);
    mkdirSync(dirname(target), { recursive: true });
    writeFileSync(target, readFileSync(join(bundle.dir, file)));
  }
}

export async function openRepairPr(input: RepairPrInput): Promise<RepairPr> {
  const { run, repoRoot, result } = input;
  const remote = input.remote ?? "origin";
  const base =
    input.base ??
    (
      await run("git", ["rev-parse", "--abbrev-ref", "HEAD"], { cwd: repoRoot })
    ).stdout.trim();
  const branch = `e2e-doctor/${slug(result.spec.name)}-${result.initial.evidence.decisionId}`;
  const nameWithOwner = (
    await run(
      "gh",
      ["repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner"],
      {
        cwd: repoRoot,
      },
    )
  ).stdout.trim();
  // github.com/…/blob/<branch>/<path>?raw=true, not raw.githubusercontent.com: the
  // blob form is served under the viewer's own session, so it renders inline in a
  // PRIVATE repo too. Measured: raw URLs 404'd anonymously on the first dogfood PRs
  // and GitHub's image proxy could not fetch them either.
  const rawBase = `https://github.com/${nameWithOwner}/blob/${branch}`;

  const worktree = join(
    repoRoot,
    ".e2e-doctor-worktrees",
    branch.replace(/\//g, "__"),
  );
  // Branch from the BASE, not from whatever the working checkout has checked out:
  // the PR must contain the repair and nothing else.
  await run("git", ["worktree", "add", "--detach", worktree, base], {
    cwd: repoRoot,
  });
  try {
    await run("git", ["checkout", "-b", branch], { cwd: worktree });

    const specTarget = join(worktree, input.specFile);
    mkdirSync(dirname(specTarget), { recursive: true });
    writeFileSync(specTarget, saveSpec(result.spec));
    (input.copyBundle ?? defaultCopyBundle)(
      input.bundle,
      join(worktree, input.bundleDir),
    );

    const title = prTitle(result);
    const body = prBody({
      untested: input.bundle.untested,
      drift: input.bundle.drift,
      result,
      diff: input.bundle.diff,
      bundleDir: posix.normalize(input.bundleDir),
      rawBase,
      frames: input.bundle.frames.map((f) => f.name),
    });
    // Never inside the worktree: it is not part of the change, and a repo's own
    // pre-commit hooks may lint the whole tree (measured: this repo's format check
    // flagged it and refused the commit).
    const bodyFile = join(
      mkdtempSync(join(tmpdir(), "e2e-doctor-pr-")),
      "pr-body.md",
    );
    writeFileSync(bodyFile, body);

    await run("git", ["add", input.specFile, input.bundleDir], {
      cwd: worktree,
    });
    await run(
      "git",
      [
        "commit",
        "-q",
        "-m",
        `${title}\n\n${result.outcome}; decision ${result.initial.evidence.decisionId}`,
      ],
      { cwd: worktree },
    );
    await run("git", ["push", "-q", "-u", remote, branch], { cwd: worktree });
    const url =
      (
        await run(
          "gh",
          [
            "pr",
            "create",
            "--base",
            base,
            "--head",
            branch,
            "--title",
            title,
            "--body-file",
            bodyFile,
          ],
          {
            cwd: worktree,
          },
        )
      ).stdout
        .trim()
        .split("\n")
        .pop() ?? "";
    return { branch, url, title, body };
  } finally {
    await run("git", ["worktree", "remove", "--force", worktree], {
      cwd: repoRoot,
    }).catch(() => {});
  }
}

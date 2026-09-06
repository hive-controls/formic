/**
 * A Playwright Reporter that, on a failed spec-backed test, runs heal — opt-in via
 * the `heal` option, off by default (healing a CI run's failure spends a healer call
 * and, with `pr`, opens a pull request: never a silent side effect of a plain run).
 *
 * Reuses the heal CLI's existing PR path rather than re-implementing it: this file
 * just spawns `e2e-doctor heal <spec> [--app <dir>] [--pr]` for the failed test's
 * spec file, found via the `formic-spec-file` annotation `defineSpecTests` stamps on
 * every generated test (fixture.mts). Everything past that point — gate selection,
 * healer selection, evidence, and (with `pr`) opening the repair PR — is the CLI's
 * own logic.
 *
 * It heals on a test's FINAL outcome only, once per spec file per run: an attempt
 * Playwright will retry is not yet a failure, and one broken spec must cost one
 * healer call and at most one pull request however many tests or projects it fails.
 */
import { execFile } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import type { Reporter, TestCase, TestResult } from "@playwright/test/reporter";
import { SPEC_FILE_ANNOTATION } from "./fixture.mts";

export { SPEC_FILE_ANNOTATION } from "./fixture.mts";

const execFileAsync = promisify(execFile);

const CLI = join(
  dirname(fileURLToPath(import.meta.url)),
  "..",
  "cli",
  import.meta.url.endsWith(".mts") ? "e2e-doctor.mts" : "e2e-doctor.mjs",
);

export interface RunHeal {
  (args: string[], env: NodeJS.ProcessEnv): Promise<{ stdout: string }>;
}

const defaultRunHeal: RunHeal = (args, env) =>
  execFileAsync("node", args, { env });

export interface HealReporterOptions {
  /** Off by default. */
  heal?: boolean;
  /** Only consulted when `heal` is also set — opens the repair PR via the CLI's own
   *  `--pr` path. */
  pr?: boolean;
  /** Forwarded as the CLI's own `--app <dir>`, so heal replays against the same
   *  application the failed test ran against rather than whatever the spec's URL
   *  happens to reach from the heal process. */
  app?: string;
  /** Merged over `process.env` for the heal spawn. Gate selection stays where it
   *  already lives — the inherited `FORMIC_GATE` — this is for the rest. */
  env?: Record<string, string>;
  /** Injectable so the spawn edge is testable without a real subprocess. */
  runHeal?: RunHeal;
}

export default class HealReporter implements Reporter {
  /** Spec files already healed in this run: a spec that fails in several projects,
   *  or whose retries all land, must not spend several healer calls — and with `pr`
   *  must not open several pull requests for one broken spec. */
  private readonly healed = new Set<string>();

  constructor(private readonly options: HealReporterOptions = {}) {}

  async onTestEnd(test: TestCase, result: TestResult): Promise<void> {
    if (!this.options.heal) return;
    // Three conditions, ALL required. The attempt itself must have failed — outcome
    // is NOT status, and a `test.fail`-annotated test that unexpectedly PASSES is
    // also "unexpected"; healing that, and burning its dedupe slot, would silently
    // skip a genuine later failure of the same spec.
    if (result.status !== "failed" && result.status !== "timedOut") return;
    // Only the FINAL attempt: one Playwright will retry is not yet a failure.
    if (result.retry < test.retries) return;
    // And only an unexpected one: an expected failure (a known `test.fail`) or a
    // flaky test (a later attempt passed) is not worth a healer call.
    if (test.outcome() !== "unexpected") return;
    const specFile = test.annotations.find(
      (annotation) => annotation.type === SPEC_FILE_ANNOTATION,
    )?.description;
    if (!specFile) return;
    if (this.healed.has(specFile)) return;
    this.healed.add(specFile);

    const loader = CLI.endsWith(".mts") ? ["--import", "tsx"] : [];
    const args = [...loader, CLI, "heal", specFile];
    if (this.options.app) args.push("--app", this.options.app);
    if (this.options.pr) args.push("--pr");
    const run = this.options.runHeal ?? defaultRunHeal;
    try {
      const { stdout } = await run(args, {
        ...process.env,
        ...(this.options.env ?? {}),
      });
      console.log(stdout);
    } catch (error) {
      const stderr = (error as { stderr?: string }).stderr;
      console.error(`heal reporter: ${stderr ?? (error as Error).message}`);
    }
  }
}

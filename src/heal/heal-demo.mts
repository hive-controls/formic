/**
 * Replay a spec and, if it breaks, heal it. The CI-shaped half of healing.
 *
 *   node --import tsx packages/harness/src/heal/heal-demo.mts <spec.yaml> [--write] [--evidence <dir>] [--app <dir>]
 *
 * The healer is chosen by env (see healers/from-env.mts): an OpenAI-compatible endpoint
 * with a model of your choosing, or a headless coding agent on your own subscription
 * (FORMIC_HEALER=agent:claude | agent:codex | agent:kimi | agent:grok | agent:custom).
 * --write overwrites the spec file with the repaired spec (or with a recorded
 * assertion proposal for review). --app <dir> hosts the app under test: the
 * spec is rebased onto the host for the run and unrebased before write-back. Exit code:
 * see cli.mts.
 */
import "dotenv/config";
import { readFileSync, writeFileSync } from "node:fs";
import { loadSpec } from "../spec/parse.mts";
import {
  describeSelection,
  preflightSpec,
  selectGate,
} from "../fleet/fleet.mts";
import { describeHostSelection, selectHost } from "../host/host.mts";
import { rebaseSpec } from "../host/rebase.mts";
import type { ExitCode } from "../replay/cli.mts";
import { redactPreviewUrl } from "../replay/evidence.mts";
import { createStatusWriter } from "../status/stream.mts";
import { runHealCli } from "./cli.mts";
import { healerFromEnv } from "./healers/from-env.mts";

async function main(argv: string[]): Promise<ExitCode> {
  const positional = argv.filter((a) => !a.startsWith("--"));
  const specFile = positional[0];
  if (!specFile) {
    console.error(
      "usage: heal-demo.mts <spec.yaml> [--write] [--evidence <dir>] [--app <dir>]",
    );
    return 2;
  }
  const status = createStatusWriter(process.env.HIVEDECK_STATUS_FILE);
  status.emit({ event: "started" });
  const write = argv.includes("--write");
  const evidenceFlag = argv.indexOf("--evidence");
  const evidenceDir = evidenceFlag >= 0 ? argv[evidenceFlag + 1] : undefined;
  const appFlag = argv.indexOf("--app");
  const appDir = appFlag >= 0 ? argv[appFlag + 1] : undefined;
  const maxAttempts = process.env.FORMIC_HEALER_MAX_ATTEMPTS
    ? Number(process.env.FORMIC_HEALER_MAX_ATTEMPTS)
    : undefined;

  let spec;
  let driver;
  let healer;
  let hosted;
  let host;
  let originalOrigin;
  try {
    spec = loadSpec(readFileSync(specFile, "utf8"));
    const gate = selectGate();
    driver = gate.driver;
    console.log(describeSelection(gate));
    if (appDir) {
      const selection = selectHost(process.env, gate);
      console.log(describeHostSelection(selection));
      hosted = await selection.host.open(process.env, appDir);
      originalOrigin = new URL(spec.startUrl).origin;
      spec = rebaseSpec(spec, hosted.baseUrl);
      host = {
        name: hosted.name,
        kind: selection.host.kind,
        baseUrl: redactPreviewUrl(hosted.baseUrl),
      };
    }
    preflightSpec(spec, gate);
    healer = healerFromEnv();
  } catch (setupError) {
    await hosted?.close().catch((closeError: unknown) => {
      console.error(`cleanup failed: ${(closeError as Error).message}`);
    });
    console.error(`could not start: ${(setupError as Error).message}`);
    status.emit({ event: "failed" });
    return 2;
  }
  return runHealCli({
    spec,
    driver,
    healer,
    io: { log: console.log, error: console.error },
    status,
    options: { maxAttempts, host },
    evidenceDir,
    writeSpec: write ? (yaml) => writeFileSync(specFile, yaml) : undefined,
    appHost: hosted
      ? { baseUrl: hosted.baseUrl, originalOrigin: originalOrigin! }
      : undefined,
    // Closing the hosted app is part of the run's own outcome, not afterthought
    // cleanup: it must finish before the terminal status is reported.
    cleanup: hosted ? () => hosted.close() : undefined,
  });
}

process.exitCode = await main(process.argv.slice(2));

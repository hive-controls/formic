/**
 * Replay a compiled spec and write its audit record. The CI-shaped half of replay.
 *
 *   node --import tsx packages/harness/src/replay/replay-demo.mts <spec.yaml> [evidence.json]
 *
 * Gate by the Fleet: Solari when SOLARI_API_KEY resolves, else local. FORMIC_GATE=local|solari (with
 * SOLARI_API_KEY in .env) to run in a recorded cloud session and get replay segments;
 * the local driver cannot record yet, and the evidence record says so rather than
 * presenting an empty segment list as a clean run.
 *
 * Exit code is the contract (see cli.mts): 0 passed, 1 the replay failed, 2 the
 * harness could not start, finish, or clean up. This file is only the argv shell.
 *
 * The code is set via process.exitCode, never process.exit(): with stdout piped (CI
 * log capture) an explicit exit discards whatever is still queued past the pipe
 * buffer — measured at 64 KiB — which is exactly the failed-step diagnostic a reader
 * needs most. Letting the loop drain keeps the output whole.
 */
import "dotenv/config";
import { readFileSync, writeFileSync } from "node:fs";
import { loadSpec } from "../spec/parse.mts";
import {
  describeSelection,
  preflightSpec,
  selectGate,
} from "../fleet/fleet.mts";
import { createStatusWriter } from "../status/stream.mts";
import { runReplayCli, type ExitCode } from "./cli.mts";

async function main(argv: string[]): Promise<ExitCode> {
  const [specFile, evidenceFile] = argv;
  if (!specFile) {
    console.error("usage: replay-demo.mts <spec.yaml> [evidence.json]");
    return 2;
  }
  const status = createStatusWriter(process.env.HIVEDECK_STATUS_FILE);
  status.emit({ event: "started" });
  let spec;
  let driver;
  try {
    spec = loadSpec(readFileSync(specFile, "utf8"));
    const gate = selectGate();
    driver = gate.driver;
    console.log(describeSelection(gate));
    preflightSpec(spec, gate);
  } catch (setupError) {
    console.error(`could not start: ${(setupError as Error).message}`);
    status.emit({ event: "failed" });
    return 2;
  }
  return runReplayCli({
    spec,
    driver,
    evidenceFile,
    io: { log: console.log, error: console.error },
    status,
    writeEvidence: writeFileSync,
  });
}

process.exitCode = await main(process.argv.slice(2));

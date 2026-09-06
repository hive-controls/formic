import { appendFileSync } from "node:fs";

export type StatusEvent =
  | { event: "started" | "progress" }
  | {
      event: "ok" | "failed" | "needs-human";
      artifact?: string;
    };

export interface StatusWriter {
  emit(event: StatusEvent): void;
}

/**
 * File-only: with `HIVEDECK_STATUS_FILE` unset, nothing is emitted anywhere. Every
 * process diagnostic (setup failures, step output) keeps going to stderr as plain
 * human-readable text — the status NDJSON never shares that stream, so a pane
 * consumer parsing it line-by-line never has to skip a non-JSON line.
 */
export function createStatusWriter(
  statusFile: string | undefined,
): StatusWriter {
  return {
    emit(event) {
      if (!statusFile) return;
      appendFileSync(statusFile, `${JSON.stringify(event)}\n`);
    },
  };
}

/**
 * Agent detection for Launchie (`e2e-doctor setup`) — is a headless coding agent on
 * PATH, and does it answer `--version` before agent-cli.mts's own 30 s probe budget
 * (agent-cli.mts:184, the same figure reused here)? Detection never spawns a login
 * command: no adapter reviewed here exposes one, so `LOGIN_NOT_CHECKED_NOTE` is the
 * honest line the wizard prints once, not a check this module performs.
 *
 * `measuredLatency` copies agent-cli.mts's own VERIFIED comments (live, 2026-09-01)
 * rather than measuring again here: claude 53/68/42 s, codex 71 s, kimi 32 s,
 * grok 83 s. A custom/unlisted adapter has none on record, so it reads null.
 *
 * `defaultDetectSeams.which` shells out to the platform's own `which`/`where` (a
 * node:child_process spawn, never `shell: true`) — ⚠️ ASSUMED present on the host;
 * `.run` spawns the adapter's own command with its `versionArgs`. Neither seam
 * throws: a missing binary, a non-zero exit, or a timed-out probe all read back as
 * "not detected" data, never an exception a caller has to catch.
 */
import { spawn } from "node:child_process";
import {
  ADAPTERS,
  type AgentCliAdapter,
} from "../../heal/healers/agent-cli.mts";

export interface DetectSeams {
  which(command: string): Promise<string | null>;
  run(
    command: string,
    args: string[],
    timeoutMs: number,
  ): Promise<{ code: number | null; stdout: string; stderr: string }>;
}

export interface DetectedAgent {
  name: string;
  onPath: boolean;
  path: string | null;
  version: string | null;
  verified: boolean;
  measuredLatency: string | null;
  loginChecked: false;
}

export const LOGIN_NOT_CHECKED_NOTE =
  "login state not checked — if the smoke heal fails with an auth error, run your agent's own login and re-run setup.";

const MEASURED_LATENCY: Record<string, string> = {
  claude: "53/68/42 s",
  codex: "71 s",
  kimi: "32 s",
  grok: "83 s",
};

const VERSION_PROBE_BUDGET_MS = 30_000;

function spawnOnce(
  command: string,
  args: string[],
  timeoutMs: number,
): Promise<{ code: number | null; stdout: string; stderr: string }> {
  return new Promise((resolveSpawn) => {
    const child = spawn(command, args, { stdio: ["ignore", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    let settled = false;
    const finish = (code: number | null) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolveSpawn({ code, stdout, stderr });
    };
    const timer = setTimeout(() => {
      child.kill("SIGKILL");
      finish(null);
    }, timeoutMs);
    child.stdout.on("data", (chunk: Buffer) => (stdout += chunk.toString()));
    child.stderr.on("data", (chunk: Buffer) => (stderr += chunk.toString()));
    child.on("error", () => finish(null));
    child.on("close", (code) => finish(code));
  });
}

async function whichOnPath(command: string): Promise<string | null> {
  const finder = process.platform === "win32" ? "where" : "which";
  const result = await spawnOnce(finder, [command], 5_000);
  if (result.code !== 0) return null;
  const firstLine = result.stdout
    .split("\n")
    .map((line) => line.trim())
    .find((line) => line !== "");
  return firstLine ?? null;
}

export const defaultDetectSeams: DetectSeams = {
  which: whichOnPath,
  run: spawnOnce,
};

function firstNonEmptyLine(text: string): string | null {
  const line = text
    .split("\n")
    .map((candidate) => candidate.trim())
    .find((candidate) => candidate !== "");
  return line ?? null;
}

async function detectOne(
  seams: DetectSeams,
  adapter: AgentCliAdapter,
): Promise<DetectedAgent> {
  const path = await seams.which(adapter.command).catch(() => null);
  const onPath = path !== null;
  let version: string | null = null;
  if (onPath) {
    const result = await seams
      .run(adapter.command, adapter.versionArgs, VERSION_PROBE_BUDGET_MS)
      .catch(() => null);
    if (result && result.code === 0) version = firstNonEmptyLine(result.stdout);
  }
  return {
    name: adapter.name,
    onPath,
    path,
    version,
    verified: adapter.verified,
    measuredLatency: MEASURED_LATENCY[adapter.name] ?? null,
    loginChecked: false,
  };
}

export async function detectAgents(
  seams: DetectSeams = defaultDetectSeams,
  adapters: Record<string, AgentCliAdapter> = ADAPTERS,
): Promise<DetectedAgent[]> {
  const out: DetectedAgent[] = [];
  for (const adapter of Object.values(adapters)) {
    out.push(await detectOne(seams, adapter));
  }
  return out;
}

export function describeAgent(agent: DetectedAgent): string {
  if (!agent.onPath) return `${agent.name}: not on PATH`;
  const versionText = agent.version ?? "unknown";
  const verifiedText = agent.verified ? ", verified" : "";
  const latencyText = agent.measuredLatency
    ? ` — measured ${agent.measuredLatency} per repair`
    : "";
  return `${agent.name}: on PATH (${versionText}${verifiedText})${latencyText}`;
}

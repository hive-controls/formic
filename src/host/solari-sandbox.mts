/**
 * Host a static app inside a Solari sandbox and expose it on a public preview URL — the
 * `solari-sandbox` FORMIC_HOST, productizing probes/host-app-in-solari.mts.
 *
 * Two bugs the probe had that this module does not carry over:
 *
 *  1. `readFileSync(file, "utf8")` decodes every uploaded file as UTF-8 text before
 *     re-encoding it for the wire — any binary asset (an image, a font) comes back
 *     corrupted. Files are read and uploaded as raw bytes (`Uint8Array`/`Buffer`).
 *  2. Nothing released the sandbox if setup failed after `create()`. Everything from
 *     `connect()` onward runs inside one try/catch whose catch races `kill()` (capped by
 *     `killTimeoutMs`) before rethrowing the original error — a setup failure never
 *     leaks a slot against the 2-concurrent Starter cap.
 *
 * `commands.start(...,{background:true})` returns before the guest's listener is
 * actually accepting connections, so `waitForReady` polls `curl`'s status code inside
 * the guest (the 500 ms backoff is a shell `sleep`, not a local timer — nothing here
 * blocks on `setTimeout` between polls) until it sees `200`, racing that poll loop
 * against the started server's own `CommandHandle.wait()` so a server that crashes
 * before it is ready fails immediately instead of after the full poll budget.
 *
 * Measured (probes/host-app-in-solari.mts against usecases/self-healing-e2e/sample-app,
 * 2026-09-02, one sandbox, killed after): previewUrl is a QUERY-token shape —
 * `https://<sandbox-id>-<port>.preview.getsolari.com?pt_token=<token>` — see
 * host/rebase.mts's header for the full redacted shape. Curl statuses across the run:
 * the tokened URL while the sandbox was live → `200`; the bare origin (no `pt_token`)
 * while live → `401` (an unauthenticated preview request is refused, not merely
 * unrouted); the SAME tokened URL, 5 s after `kill()` → `404` — a token that reaches
 * evidence is dead once the run's sandbox is gone (✅ VERIFIED, not merely assumed).
 * Even so, a repair PR commits the bundle, so bundle internals (initial.json, rrweb
 * segments, the page) are unrebased at write time (`unrebaseText`) and the audit row is
 * redacted: a public PR shows the app's real origin, never a dead token or preview host.
 */
import { randomBytes } from "node:crypto";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { basename, relative, sep } from "node:path";
import { SandboxClient } from "@solarisdk/sandbox";
import type { GuestIdentity, Host, HostedApp } from "./host.mts";

const DEFAULT_SOLARI_BASE_URL = "https://api.getsolari.com";
const DEFAULT_PORT = 4173;
const GUEST_ROOT = "/tmp/app";
const SANDBOX_TIMEOUT_MS = 30 * 60 * 1000;
const DEFAULT_KILL_TIMEOUT_MS = 20_000;
const DEFAULT_READY_ATTEMPTS = 30;
const READY_POLL_INTERVAL_MS = 500;

/** A started, backgrounded in-guest command — the slice of the SDK's CommandHandle this
 *  module uses to race readiness polling against an early exit. */
export interface SandboxCommandHandleLike {
  wait(): Promise<number>;
  kill(signal?: number): Promise<void>;
}

/** The slice of the Solari sandbox SDK this module uses — injectable so setup-failure
 *  and readiness-poll paths are testable with no key and no network, mirroring
 *  driver/solari.mts's `SolariClientLike`. */
export interface SandboxLike {
  readonly id: string;
  connect(): Promise<void>;
  files: {
    mkdir(path: string): Promise<void>;
    upload(path: string, data: Uint8Array | string): Promise<void>;
  };
  commands: {
    run(
      cmd: string,
      opts?: { args?: string[]; cwd?: string },
    ): Promise<{ exitCode: number; stdout: string; stderr: string }>;
    start(
      cmd: string,
      opts?: { args?: string[]; cwd?: string; background?: boolean },
    ): Promise<SandboxCommandHandleLike>;
  };
  previewUrl(port: number): Promise<{ url: string; token?: string }>;
  kill(): Promise<void>;
}

export interface SandboxClientLike {
  create(opts: {
    metadata?: Record<string, string>;
    timeoutMs?: number;
  }): Promise<SandboxLike>;
}

export interface SandboxIo {
  log(line: string): void;
}

const silentIo: SandboxIo = { log() {} };

const MACHINE_ID_PATTERN = /^[0-9a-f]{32}$/;

export function isMachineIdFormat(value: string): boolean {
  return MACHINE_ID_PATTERN.test(value);
}

/** The slice of a connected sandbox needed to regenerate /etc/machine-id. */
export interface GuestIdentityTarget {
  readonly id: string;
  commands: SandboxLike["commands"];
}

/** Write a fresh 32-hex id into the guest's /etc/machine-id and read it back.
 *  Call after `connect()`, before any adopter code. `boot_id` cannot be changed
 *  from inside a container and is not attempted. */
export async function neutraliseGuestMachineId(
  sandbox: GuestIdentityTarget,
  io: SandboxIo = silentIo,
): Promise<GuestIdentity> {
  const machineId = randomBytes(16).toString("hex");
  const written = await sandbox.commands.run("sh", {
    args: [
      "-c",
      `printf '%s\\n' '${machineId}' > /etc/machine-id && cat /etc/machine-id`,
    ],
  });
  if (written.exitCode !== 0) {
    const detail = (written.stderr || written.stdout).trim() || "no output";
    throw new Error(
      `could not write /etc/machine-id (exit ${written.exitCode}): ${detail}`,
    );
  }
  const readBack = written.stdout.trim();
  if (readBack !== machineId || !isMachineIdFormat(readBack)) {
    throw new Error(
      `guest /etc/machine-id read-back was ${JSON.stringify(readBack)}, expected ${machineId}`,
    );
  }
  const identity: GuestIdentity = { sandboxId: sandbox.id, machineId };
  io.log(
    `guest identity sandbox=${identity.sandboxId} machine-id=${identity.machineId}`,
  );
  return identity;
}

export interface OpenSolariSandboxOptions {
  apiKey: string;
  baseUrl?: string;
  port?: number;
  client?: SandboxClientLike;
  /** Total readiness-poll budget; overrides the default ~30-attempt / 15 s ceiling. */
  readyTimeoutMs?: number;
  killTimeoutMs?: number;
}

function collectFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const full = `${dir}/${name}`;
    return statSync(full).isDirectory() ? collectFiles(full) : [full];
  });
}

function guestPathFor(appDir: string, guestRoot: string, file: string): string {
  return `${guestRoot}/${relative(appDir, file).split(sep).join("/")}`;
}

function ancestorDirs(guestPath: string, guestRoot: string): string[] {
  const parts = guestPath
    .slice(guestRoot.length + 1)
    .split("/")
    .slice(0, -1);
  const dirs: string[] = [];
  let acc = guestRoot;
  for (const part of parts) {
    acc = `${acc}/${part}`;
    dirs.push(acc);
  }
  return dirs;
}

async function uploadDir(
  sandbox: SandboxLike,
  appDir: string,
  guestRoot: string,
): Promise<void> {
  const files = collectFiles(appDir);
  const dirs = new Set<string>([guestRoot]);
  for (const file of files) {
    for (const dir of ancestorDirs(
      guestPathFor(appDir, guestRoot, file),
      guestRoot,
    )) {
      dirs.add(dir);
    }
  }
  const shallowestFirst = [...dirs].sort(
    (a, b) => a.split("/").length - b.split("/").length,
  );
  for (const dir of shallowestFirst) await sandbox.files.mkdir(dir);
  for (const file of files) {
    await sandbox.files.upload(
      guestPathFor(appDir, guestRoot, file),
      readFileSync(file),
    );
  }
}

/** The identical inline static server the probe used — python3's own `http.server`
 *  needs no bespoke script, but not every image ships python3. */
function nodeStaticServerScript(port: number): string {
  return `const h=require("http"),f=require("fs"),p=require("path");h.createServer((q,r)=>{const u=q.url==="/"?"/index.html":q.url.split("?")[0];const fp=p.join(process.cwd(),u);f.readFile(fp,(e,d)=>{if(e){r.writeHead(404);r.end();return;}r.writeHead(200,{"content-type":u.endsWith(".js")?"text/javascript":u.endsWith(".css")?"text/css":"text/html"});r.end(d);});}).listen(${port},"0.0.0.0");`;
}

async function startServer(
  sandbox: SandboxLike,
  guestRoot: string,
  port: number,
): Promise<SandboxCommandHandleLike> {
  const python = await sandbox.commands.run("sh", {
    args: ["-c", "command -v python3"],
  });
  if (python.exitCode === 0) {
    return sandbox.commands.start("python3", {
      args: ["-m", "http.server", String(port), "--bind", "0.0.0.0"],
      cwd: guestRoot,
      background: true,
    });
  }
  return sandbox.commands.start("node", {
    args: ["-e", nodeStaticServerScript(port)],
    cwd: guestRoot,
    background: true,
  });
}

async function waitForReady(
  sandbox: SandboxLike,
  port: number,
  server: SandboxCommandHandleLike,
  attempts: number,
): Promise<void> {
  const serverExited = server.wait().then((exitCode) => {
    throw new Error(
      `app server on port ${port} exited (code ${exitCode}) before it became ready`,
    );
  });
  const polled = (async () => {
    for (let attempt = 1; attempt <= attempts; attempt++) {
      const probe = await sandbox.commands.run("sh", {
        args: [
          "-c",
          `sleep ${READY_POLL_INTERVAL_MS / 1000}; curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:${port}/`,
        ],
      });
      if (probe.stdout.trim() === "200") return;
      if (attempt === attempts) {
        throw new Error(
          `app server on port ${port} never became ready after ${attempts} attempts (last status ${probe.stdout.trim() || "none"})`,
        );
      }
    }
  })();
  await Promise.race([polled, serverExited]);
}

function closeSandbox(
  sandbox: SandboxLike,
  killTimeoutMs: number,
): () => Promise<void> {
  return async () => {
    // A bare `setTimeout` losing the race still fires later and holds the event loop
    // open until then — clear it as soon as either side settles, or a caller that
    // awaits close() and then expects the process to exit finds it lingering.
    let timer!: NodeJS.Timeout;
    const timeout = new Promise<void>((resolve) => {
      timer = setTimeout(resolve, killTimeoutMs);
    });
    try {
      await Promise.race([sandbox.kill().catch(() => {}), timeout]);
    } finally {
      clearTimeout(timer);
    }
  };
}

/** Upload `appDir` into a fresh Solari sandbox, serve it, and hand back its public
 *  preview URL. Everything after `create()` is covered by one catch that kills the
 *  sandbox before rethrowing — see the file header. */
export async function openSolariSandbox(
  appDir: string,
  options: OpenSolariSandboxOptions,
  io: SandboxIo = silentIo,
): Promise<HostedApp> {
  const client: SandboxClientLike =
    options.client ??
    new SandboxClient({
      apiKey: options.apiKey,
      baseUrl: options.baseUrl ?? DEFAULT_SOLARI_BASE_URL,
    });
  const port = options.port ?? DEFAULT_PORT;
  const killTimeoutMs = options.killTimeoutMs ?? DEFAULT_KILL_TIMEOUT_MS;
  const readyAttempts = options.readyTimeoutMs
    ? Math.max(1, Math.ceil(options.readyTimeoutMs / READY_POLL_INTERVAL_MS))
    : DEFAULT_READY_ATTEMPTS;

  const sandbox = await client.create({
    metadata: { tool: "e2e-doctor", app: basename(appDir) },
    timeoutMs: SANDBOX_TIMEOUT_MS,
  });
  try {
    await sandbox.connect();
    io.log(`sandbox ${sandbox.id} connected`);
    const guestIdentity = await neutraliseGuestMachineId(sandbox, io);
    await uploadDir(sandbox, appDir, GUEST_ROOT);
    const server = await startServer(sandbox, GUEST_ROOT, port);
    await waitForReady(sandbox, port, server, readyAttempts);
    const preview = await sandbox.previewUrl(port);
    return {
      name: "solari-sandbox",
      baseUrl: preview.url,
      close: closeSandbox(sandbox, killTimeoutMs),
      guestIdentity,
    };
  } catch (error) {
    await closeSandbox(sandbox, killTimeoutMs)();
    throw error;
  }
}

export const solariSandboxHost: Host = {
  name: "solari-sandbox",
  kind: "Outside",
  presence(env) {
    return env.SOLARI_API_KEY
      ? { ready: true, reason: "SOLARI_API_KEY resolves" }
      : { ready: false, reason: "SOLARI_API_KEY is not set" };
  },
  open(env, appDir) {
    return openSolariSandbox(appDir, {
      apiKey: env.SOLARI_API_KEY as string,
      baseUrl: env.SOLARI_BASE_URL,
    });
  },
};

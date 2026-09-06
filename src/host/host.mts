/**
 * The app host — where the app under test runs from.
 *
 * A gate picks WHO runs the browser (Solari or the local machine); a host picks WHERE
 * the app under test is served from, and the two are coupled: an Outside gate cannot
 * reach `localhost` at all (fleet.mts's `preflightSpec` refuses it before a session
 * opens), so a run with `--app <dir>` on Solari needs the app hosted somewhere Solari
 * CAN reach — a sandbox preview URL. The rules mirror the Fleet's:
 *
 *   - `FORMIC_HOST` names the host explicitly and always wins. A typo is refused.
 *   - Unset, the host follows the gate: Outside → `solari-sandbox`, Inside → `local`.
 *     This is a direct mapping, not a preference-order search like the Fleet's own
 *     default — a followed-but-unready host throws, it never silently falls back to the
 *     other host (that would mean the announced gate and the actual app location
 *     disagree, which is worse than refusing).
 */
import type { GateKind, GateSelection } from "../fleet/fleet.mts";
import { localHost } from "./local.mts";
import { solariSandboxHost } from "./solari-sandbox.mts";

export const HOST_VAR = "FORMIC_HOST";

export type HostName = "solari-sandbox" | "local";

export interface Presence {
  ready: boolean;
  /** Human-readable, printed verbatim in the announcement or the refusal. */
  reason: string;
}

/** Mapping of a Solari sandbox to the guest /etc/machine-id written at session
 *  start. Auditable operator record; not a kernel boot identity. */
export interface GuestIdentity {
  sandboxId: string;
  machineId: string;
}

export interface HostedApp {
  readonly name: HostName;
  /** e.g. `http://127.0.0.1:53211` or a Solari sandbox's tokened preview URL. */
  readonly baseUrl: string;
  close(): Promise<void>;
  /** Set when this host opened a Solari sandbox with a distinct machine identity. */
  readonly guestIdentity?: GuestIdentity;
}

export interface Host {
  readonly name: HostName;
  readonly kind: GateKind;
  presence(env: NodeJS.ProcessEnv): Presence;
  open(env: NodeJS.ProcessEnv, appDir: string): Promise<HostedApp>;
}

/** Preference mirrors the Fleet's GATES order: Solari first-party, local needs no key. */
export const HOSTS: readonly Host[] = [solariSandboxHost, localHost];

export interface HostSelection {
  host: Host;
  how: "explicit" | "default";
  reason: string;
}

function defaultHostFor(gate: GateSelection): Host {
  return gate.gate.kind === "Outside" ? solariSandboxHost : localHost;
}

/** Pick the host for this run. `gate` is the already-selected Fleet gate — the default
 *  host follows it directly, with no fallback traversal (see the file header). */
export function selectHost(
  env: NodeJS.ProcessEnv = process.env,
  gate: GateSelection,
): HostSelection {
  const requested = (env[HOST_VAR] ?? "").trim();
  const names = HOSTS.map((host) => `"${host.name}"`).join(" or ");
  if (requested) {
    const host = HOSTS.find((candidate) => candidate.name === requested);
    if (!host) {
      throw new Error(
        `unsupported ${HOST_VAR} "${requested}" (expected ${names})`,
      );
    }
    const presence = host.presence(env);
    if (!presence.ready) {
      throw new Error(`${HOST_VAR}=${host.name} but ${presence.reason}`);
    }
    return { host, how: "explicit", reason: `${HOST_VAR}=${host.name}` };
  }
  const host = defaultHostFor(gate);
  const presence = host.presence(env);
  if (!presence.ready) {
    throw new Error(
      `${HOST_VAR} would default to ${host.name} (following the ${gate.gate.name} gate) but ${presence.reason}`,
    );
  }
  return {
    host,
    how: "default",
    reason: `default: follows the gate (${gate.gate.name}, ${gate.gate.kind})`,
  };
}

/** The one line a run prints before it opens the app host. */
export function describeHostSelection(selection: HostSelection): string {
  return `host: ${selection.host.name} (${selection.host.kind}) — ${selection.reason}`;
}

export {
  isMachineIdFormat,
  neutraliseGuestMachineId,
} from "./solari-sandbox.mts";

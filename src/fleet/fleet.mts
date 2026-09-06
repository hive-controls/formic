/**
 * The Fleet — one mind, many gates.
 *
 * A gate is a backend adapter: Solari (Outside — runs remotely, records server-side),
 * a trial cloud-grid vendor (Outside — BrowserStack Automate or Sauce Labs, records via
 * injected rrweb, we own the page either way), or the local browser (Inside — runs on
 * this machine, records via injected rrweb). The Fleet picks one gate per run. The
 * rules are the product:
 *
 *   - `FORMIC_GATE` names the gate explicitly and always wins. A typo is refused, never
 *     silently local: a CI run meant to produce a cloud recording would otherwise pass
 *     green with no recording at all.
 *   - Unset, the first READY gate among the DEFAULT-ELIGIBLE ones is chosen: Solari
 *     when its key resolves, else local. Solari is first-party, never load-bearing —
 *     the flagship never steers the fleet. `browserstack` and `saucelabs` are
 *     explicit-only (`defaultEligible: false`): a third-party trial vendor must never
 *     silently steer the fleet either — ambient BROWSERSTACK_ and SAUCE_ credentials
 *     (common on a shared CI runner) would otherwise select a paid session nobody
 *     asked for. Name one with `FORMIC_GATE=browserstack|saucelabs` to use it.
 *   - Whatever was chosen is announced in one line before anything runs.
 *
 * App hosting (`FORMIC_HOST`, ../host/host.mts) is unsolved for the two trial gates —
 * its default-host mapping is Outside → solari-sandbox, which needs SOLARI_API_KEY, not
 * a BrowserStack/Sauce credential. `--app <dir>` under an explicit
 * FORMIC_GATE=browserstack|saucelabs needs `FORMIC_HOST` set to a host that vendor can
 * reach (or a public URL spec with no `--app` at all) until a trial-vendor host lands.
 */
import { existsSync } from "node:fs";
import { chromium } from "playwright-core";
import type { Driver } from "../driver/types.mts";
import { LocalPlaywrightDriver } from "../driver/local-playwright.mts";
import { SolariDriver } from "../driver/solari.mts";
import { BrowserStackDriver } from "../driver/browserstack.mts";
import { SauceLabsDriver } from "../driver/saucelabs.mts";
import type { Spec } from "../spec/types.mts";

export const GATE_VAR = "FORMIC_GATE";

export type GateName = "solari" | "local" | "browserstack" | "saucelabs";
/** Outside gates run remotely; Inside gates run on this machine. */
export type GateKind = "Outside" | "Inside";

export interface Presence {
  ready: boolean;
  /** Human-readable, printed verbatim in the announcement or the refusal. */
  reason: string;
}

/** Facts about this machine the presence checks need; injectable for tests. */
export interface FleetProbes {
  localBrowserInstalled(): boolean;
}

export interface Gate {
  readonly name: GateName;
  readonly kind: GateKind;
  /** Whether the default (no `FORMIC_GATE`) preference search considers this gate. */
  readonly defaultEligible: boolean;
  presence(env: NodeJS.ProcessEnv, probes: FleetProbes): Presence;
  open(env: NodeJS.ProcessEnv): Driver;
}

const solariGate: Gate = {
  name: "solari",
  kind: "Outside",
  defaultEligible: true,
  presence(env) {
    return env.SOLARI_API_KEY
      ? { ready: true, reason: "SOLARI_API_KEY resolves" }
      : { ready: false, reason: "SOLARI_API_KEY is not set" };
  },
  open(env) {
    return new SolariDriver({ apiKey: env.SOLARI_API_KEY as string });
  },
};

const localGate: Gate = {
  name: "local",
  kind: "Inside",
  defaultEligible: true,
  presence(_env, probes) {
    return probes.localBrowserInstalled()
      ? { ready: true, reason: "Chromium is installed" }
      : {
          ready: false,
          reason:
            "Chromium is not installed for playwright-core (run: npx playwright install chromium)",
        };
  },
  open(env) {
    // Headless by default — a replay wants no window. `FORMIC_HEADED=1` is what the
    // `record` command sets: a human cannot click a browser they cannot see.
    return new LocalPlaywrightDriver({
      headless: env.FORMIC_HEADED === "1" ? false : undefined,
    });
  },
};

/** Both credential vars must resolve — a vendor session opened with only one is a
 *  confusing partial-auth failure from the vendor's own API, not a clean local refusal. */
function credentialPairPresence(
  env: NodeJS.ProcessEnv,
  usernameVar: string,
  accessKeyVar: string,
): Presence {
  const missing = [usernameVar, accessKeyVar].filter((name) => !env[name]);
  if (missing.length === 0) {
    return {
      ready: true,
      reason: `${usernameVar} and ${accessKeyVar} resolve`,
    };
  }
  return { ready: false, reason: `${missing.join(" and ")} not set` };
}

const browserstackGate: Gate = {
  name: "browserstack",
  kind: "Outside",
  defaultEligible: false,
  presence(env) {
    return credentialPairPresence(
      env,
      "BROWSERSTACK_USERNAME",
      "BROWSERSTACK_ACCESS_KEY",
    );
  },
  open(env) {
    return new BrowserStackDriver({
      username: env.BROWSERSTACK_USERNAME as string,
      accessKey: env.BROWSERSTACK_ACCESS_KEY as string,
    });
  },
};

const saucelabsGate: Gate = {
  name: "saucelabs",
  kind: "Outside",
  defaultEligible: false,
  presence(env) {
    return credentialPairPresence(env, "SAUCE_USERNAME", "SAUCE_ACCESS_KEY");
  },
  open(env) {
    return new SauceLabsDriver({
      username: env.SAUCE_USERNAME as string,
      accessKey: env.SAUCE_ACCESS_KEY as string,
    });
  },
};

/** Preference order for the default (no `FORMIC_GATE`) search: solari first-party,
 *  local the fallback that needs no key — both default-eligible. browserstack and
 *  saucelabs are listed (for explicit lookup, the typo message, and the "no gate
 *  ready" report) but `defaultEligible: false` keeps them out of that search. */
export const GATES: readonly Gate[] = [
  solariGate,
  localGate,
  browserstackGate,
  saucelabsGate,
];

export interface GateSelection {
  gate: Gate;
  driver: Driver;
  how: "explicit" | "default";
  reason: string;
}

export const defaultProbes: FleetProbes = {
  localBrowserInstalled: () => existsSync(chromium.executablePath()),
};

export function selectGate(
  env: NodeJS.ProcessEnv = process.env,
  probes: FleetProbes = defaultProbes,
): GateSelection {
  const requested = (env[GATE_VAR] ?? "").trim();
  const names = GATES.map((gate) => `"${gate.name}"`).join(" or ");
  if (requested) {
    const gate = GATES.find((candidate) => candidate.name === requested);
    if (!gate) {
      throw new Error(
        `unsupported ${GATE_VAR} "${requested}" (expected ${names})`,
      );
    }
    const presence = gate.presence(env, probes);
    if (!presence.ready) {
      throw new Error(`${GATE_VAR}=${gate.name} but ${presence.reason}`);
    }
    return {
      gate,
      driver: gate.open(env),
      how: "explicit",
      reason: `${GATE_VAR}=${gate.name}`,
    };
  }
  const skipped: string[] = [];
  for (const gate of GATES.filter((candidate) => candidate.defaultEligible)) {
    const presence = gate.presence(env, probes);
    if (presence.ready) {
      const because = skipped.length > 0 ? ` (${skipped.join("; ")})` : "";
      return {
        gate,
        driver: gate.open(env),
        how: "default",
        reason: `default: ${presence.reason}${because}`,
      };
    }
    skipped.push(`${gate.name}: ${presence.reason}`);
  }
  throw new Error(`no gate is ready — ${skipped.join("; ")}`);
}

/** The one line every run prints first, so a run is never silently local. */
export function describeSelection(selection: GateSelection): string {
  return `gate: ${selection.gate.name} (${selection.gate.kind}) — ${selection.reason}`;
}

/** Hosts an Outside gate can never reach: loopback, link-local, RFC 1918, IPv6 ULA,
 *  and the reserved `localhost` / `*.localhost` names. */
export function isPrivateHost(hostname: string): boolean {
  const host = hostname.replace(/^\[|\]$/g, "").toLowerCase();
  if (host === "localhost" || host.endsWith(".localhost")) return true;
  if (host.includes(":")) {
    return host === "::1" || /^fe[89ab]/.test(host) || /^f[cd]/.test(host);
  }
  const octets = host.match(/^(\d+)\.(\d+)\.(\d+)\.(\d+)$/);
  if (!octets) return false;
  const a = Number(octets[1]);
  const b = Number(octets[2]);
  return (
    a === 127 ||
    a === 10 ||
    a === 0 ||
    (a === 169 && b === 254) ||
    (a === 172 && b >= 16 && b <= 31) ||
    (a === 192 && b === 168)
  );
}

/**
 * Refuse, BEFORE any session opens, a spec an Outside gate could never reach. Measured:
 * without this the first thing a new user with a Solari key sees is a cloud session
 * spent on ERR_CONNECTION_REFUSED against their own localhost — a product that looks
 * broken from the start. Every goto target is checked, not only startUrl.
 */
export function preflightSpec(spec: Spec, selection: GateSelection): void {
  if (selection.gate.kind !== "Outside") return;
  const targets = [{ label: "startUrl", url: spec.startUrl }];
  for (const step of spec.steps) {
    if (step.action === "goto" && step.target) {
      targets.push({ label: `step ${step.index} goto`, url: step.target });
    }
  }
  for (const { label, url } of targets) {
    let hostname: string;
    try {
      hostname = new URL(url).hostname;
    } catch {
      continue; // URL shape is the spec validator's job, not the Fleet's
    }
    if (!isPrivateHost(hostname)) continue;
    throw new Error(
      `${label} ${url} is on a private or loopback host — not reachable from the ${selection.gate.name} gate (${selection.gate.kind}). Host the app where Outside can reach it (--app <dir>, a Solari sandbox previewUrl, a tunnel) or run it Inside with FORMIC_GATE=local.`,
    );
  }
}

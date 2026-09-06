/**
 * The Fleet — one mind, many gates.
 *
 * A gate is a backend choice: Solari (Outside — runs remotely, records server-side),
 * a trial cloud-grid vendor (Outside — BrowserStack Automate or Sauce Labs), or the
 * local browser (Inside — runs on this machine). The Fleet picks one gate per run and
 * resolves it into CONFIGURATION: a small map of environment variables the recipe
 * already declares. It opens nothing, connects to nothing, and imports no recipe —
 * that is the whole difference between a configurator and an engine.
 *
 *   - `FORMIC_GATE` names the gate explicitly and always wins. A typo is refused,
 *     never silently local: a CI run meant to produce a cloud recording would
 *     otherwise pass green with no recording at all.
 *   - Unset, the first READY gate among the DEFAULT-ELIGIBLE ones is chosen: Solari
 *     when its key resolves, else local. Solari is first-party, never load-bearing —
 *     the flagship never steers the fleet. `browserstack` and `saucelabs` are
 *     explicit-only (`defaultEligible: false`): a third-party trial vendor must never
 *     silently steer the fleet either — ambient BROWSERSTACK_ and SAUCE_ credentials
 *     (common on a shared CI runner) would otherwise select a paid session nobody
 *     asked for. Name one with `FORMIC_GATE=browserstack|saucelabs` to use it.
 *   - Whatever was chosen is announced in one line before anything runs.
 *
 * The two vendor gates resolve through the recipe's GENERIC remote seams, not through
 * vendor code: BrowserStack to `…_GATE=cdp` + `…_CDP_URL`, Sauce Labs to `…_GATE=local`
 * plus Playwright's own two Selenium-Grid variables (its only documented Playwright
 * route — see remote/saucelabs.mts). Neither is exercised live; both builders say so.
 *
 * App hosting (`…_HOST`) is unsolved for the two trial gates — the recipe's default
 * host mapping is Outside → its own sandbox, which needs SOLARI_API_KEY, not a
 * BrowserStack/Sauce credential. Serving an app under one of them needs `…_HOST` set to
 * a host that vendor can reach (or a public URL spec with no local app at all).
 */
import {
  DEFAULT_CONFIG_ENV_PREFIX,
  configKey,
  readConfigValue,
} from "../config/keys.mts";
import { buildBrowserStackWsEndpoint } from "../remote/browserstack.mts";
import { buildSauceRemoteGrid } from "../remote/saucelabs.mts";

export const GATE_VAR = "FORMIC_GATE";

export type GateName = "solari" | "local" | "browserstack" | "saucelabs";
/** Outside gates run remotely; Inside gates run on this machine. */
export type GateKind = "Outside" | "Inside";

export interface Presence {
  ready: boolean;
  /** Human-readable, printed verbatim in the announcement or the refusal. */
  reason: string;
}

export interface Gate {
  readonly name: GateName;
  readonly kind: GateKind;
  /** Whether the default (no `FORMIC_GATE`) preference search considers this gate. */
  readonly defaultEligible: boolean;
  presence(env: NodeJS.ProcessEnv): Presence;
  /** The configuration this choice resolves to, keyed by the recipe's own prefix. */
  config(env: NodeJS.ProcessEnv, prefix: string): Record<string, string>;
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
  config(_env, prefix) {
    return { [configKey("GATE", prefix)]: "solari" };
  },
};

const localGate: Gate = {
  name: "local",
  kind: "Inside",
  defaultEligible: true,
  presence() {
    // Always ready. Whether a browser is actually installed is the recipe's own
    // precondition, checked by the recipe with its own install instructions — a
    // configurator that duplicated that check would need the recipe's browser
    // dependency, and would go stale the moment the recipe changed engines.
    return { ready: true, reason: "runs on this machine" };
  },
  config(_env, prefix) {
    return { [configKey("GATE", prefix)]: "local" };
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
  config(env, prefix) {
    return {
      [configKey("GATE", prefix)]: "cdp",
      [configKey("CDP_URL", prefix)]: buildBrowserStackWsEndpoint({
        username: env.BROWSERSTACK_USERNAME as string,
        accessKey: env.BROWSERSTACK_ACCESS_KEY as string,
      }),
    };
  },
};

const saucelabsGate: Gate = {
  name: "saucelabs",
  kind: "Outside",
  defaultEligible: false,
  presence(env) {
    return credentialPairPresence(env, "SAUCE_USERNAME", "SAUCE_ACCESS_KEY");
  },
  config(env, prefix) {
    const grid = buildSauceRemoteGrid({
      username: env.SAUCE_USERNAME as string,
      accessKey: env.SAUCE_ACCESS_KEY as string,
    });
    return {
      // Playwright routes an ORDINARY local launch through the grid when these two
      // are set, so the recipe's local gate is the right target — Sauce offers no
      // CDP endpoint to point the cdp gate at.
      [configKey("GATE", prefix)]: "local",
      SELENIUM_REMOTE_URL: grid.url,
      SELENIUM_REMOTE_CAPABILITIES: grid.capabilities,
    };
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
  /** The environment this choice resolves to — never a driver, never a connection. */
  config: Record<string, string>;
  how: "explicit" | "default";
  reason: string;
}

export function selectGate(
  env: NodeJS.ProcessEnv = process.env,
  prefix: string = DEFAULT_CONFIG_ENV_PREFIX,
): GateSelection {
  const requested = (
    env[GATE_VAR] ??
    readConfigValue("GATE", env, prefix) ??
    ""
  )
    .toString()
    .trim();
  const names = GATES.map((gate) => `"${gate.name}"`).join(" or ");
  if (requested) {
    const gate = GATES.find((candidate) => candidate.name === requested);
    if (!gate) {
      throw new Error(
        `unsupported ${GATE_VAR} "${requested}" (expected ${names})`,
      );
    }
    const presence = gate.presence(env);
    if (!presence.ready) {
      throw new Error(`${GATE_VAR}=${gate.name} but ${presence.reason}`);
    }
    return {
      gate,
      config: gate.config(env, prefix),
      how: "explicit",
      reason: `${GATE_VAR}=${gate.name}`,
    };
  }
  const skipped: string[] = [];
  for (const gate of GATES.filter((candidate) => candidate.defaultEligible)) {
    const presence = gate.presence(env);
    if (presence.ready) {
      const because = skipped.length > 0 ? ` (${skipped.join("; ")})` : "";
      return {
        gate,
        config: gate.config(env, prefix),
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

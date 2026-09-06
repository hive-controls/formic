/**
 * The GitHub Actions snippet Launchie prints after a successful smoke heal — printed
 * only, never written to disk (setup.mts). Every secret it names is a NAME to create
 * in the repo's Settings, never a value.
 *
 * Agent profiles carry an honest caveat instead of a second secret: Claude Code and
 * Codex are reported to run headless in CI on a vendor token, but no adapter here has
 * had that token's exact variable name verified live (agent-cli.mts's own
 * VERIFICATION STATUS convention) — Kimi and Grok are unchecked entirely. An api
 * profile's `apiKeyFrom: env.NAME` names a real, resolvable secret instead.
 */
import type { HealerProfile } from "../../heal/profiles/profiles.mts";

const AGENT_CI_NOTE =
  "Claude Code and Codex are reported to run headless in CI with a vendor token (the exact secret name is not verified here — check the agent's own docs). Kimi and Grok: unchecked.";

function apiKeyEnvName(profile: HealerProfile): string | null {
  if (profile.kind !== "api" || !profile.apiKeyFrom) return null;
  return profile.apiKeyFrom.slice("env.".length);
}

export type SnippetGate = "solari" | "local";

/** An api profile pointing at a plain-http or single-label host is a LAN server — a
 *  hosted runner cannot reach it, and the snippet must say so rather than fail later. */
function lanBaseUrl(profile: HealerProfile): string | null {
  if (profile.kind !== "api" || !profile.baseUrl) return null;
  try {
    const url = new URL(profile.baseUrl);
    return url.protocol === "http:" || !url.hostname.includes(".")
      ? profile.baseUrl
      : null;
  } catch {
    return null;
  }
}

function commentBlock(profile: HealerProfile, gate: SnippetGate): string {
  const lines = [
    "# GitHub Actions secrets to create (repo Settings -> Secrets and variables -> Actions):",
  ];
  if (gate === "solari") lines.push("#   SOLARI_API_KEY");
  const apiKeyName = apiKeyEnvName(profile);
  if (apiKeyName) lines.push(`#   ${apiKeyName}`);
  if (!apiKeyName && gate === "local")
    lines.push(
      "#   (none — the local gate and a keyless healer need no secret)",
    );
  if (profile.kind === "agent") lines.push(`# ${AGENT_CI_NOTE}`);
  const lan = lanBaseUrl(profile);
  if (lan)
    lines.push(
      `# ${lan} looks like a server on your own network — a hosted runner cannot reach it; use a self-hosted runner or a reachable endpoint.`,
    );
  return lines.join("\n");
}

function envBlock(profile: HealerProfile, gate: SnippetGate): string {
  const lines = [`          FORMIC_GATE: ${gate}`];
  if (gate === "solari")
    lines.push("          SOLARI_API_KEY: ${{ secrets.SOLARI_API_KEY }}");
  const apiKeyName = apiKeyEnvName(profile);
  if (apiKeyName) {
    lines.push(`          ${apiKeyName}: \${{ secrets.${apiKeyName} }}`);
  }
  return lines.join("\n");
}

/** The gate the snippet targets follows the one `setup` ran under: a team that
 *  configured a local, keyless healer must not be handed a cloud-gate job. */
export function actionsSnippet(
  profileName: string,
  profile: HealerProfile,
  gate: SnippetGate = "solari",
): string {
  const browserStep =
    gate === "local"
      ? "\n      - run: npx playwright install --with-deps chromium"
      : "";
  return `${commentBlock(profile, gate)}
name: e2e-doctor
on: [push, pull_request]
jobs:
  e2e-doctor:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 22
      - run: npm ci${browserStep}
      - run: npm run e2e-doctor -- heal <spec.yaml> --app <app-dir> --healer ${profileName} --bundle evidence
        env:
${envBlock(profile, gate)}
      - uses: actions/upload-artifact@v4
        if: always()
        with:
          name: evidence
          path: evidence/
`;
}

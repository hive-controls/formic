/**
 * The local host — serves an app directory over loopback for the Inside gate.
 *
 * Wraps `serveDirectory` (replay/sample-app-server.mts): no key, no network, no image
 * pull, so it is always present. A local server that failed to start would be a bug in
 * this process, not a missing credential — `presence` never reports unready.
 */
import { serveDirectory } from "../replay/sample-app-server.mts";
import type { Host, HostedApp } from "./host.mts";

export const localHost: Host = {
  name: "local",
  kind: "Inside",
  presence() {
    return { ready: true, reason: "always available" };
  },
  async open(_env, appDir): Promise<HostedApp> {
    const app = await serveDirectory(appDir);
    return { name: "local", baseUrl: app.baseUrl, close: app.close };
  },
};

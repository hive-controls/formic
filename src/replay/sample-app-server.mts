/**
 * Serve a static directory over node:http on an ephemeral port.
 *
 * Backs two callers: the runner tests/demos (no external server, no fixed port — a
 * fixed :4173 would make the suite fail whenever a developer has the Playwright
 * webServer up) and the `local` FORMIC_HOST (host/local.mts), the Inside gate's way of
 * hosting an app under test with no network dependency.
 */
import { createServer, type Server } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize } from "node:path";
import type { AddressInfo } from "node:net";

const CONTENT_TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".json": "application/json; charset=utf-8",
};

export interface StaticApp {
  /** e.g. `http://127.0.0.1:53211` — no trailing slash. */
  baseUrl: string;
  close(): Promise<void>;
}

export async function serveDirectory(directory: string): Promise<StaticApp> {
  const server: Server = createServer(async (request, response) => {
    const requestPath = new URL(request.url ?? "/", "http://localhost")
      .pathname;
    const relative = requestPath === "/" ? "index.html" : requestPath.slice(1);
    // normalize() collapses `..`, so a request cannot escape the served directory.
    const filePath = join(directory, normalize(`/${relative}`));
    try {
      const body = await readFile(filePath);
      response.writeHead(200, {
        "content-type":
          CONTENT_TYPES[extname(filePath)] ?? "application/octet-stream",
        "cache-control": "no-store",
      });
      response.end(body);
    } catch {
      response.writeHead(404);
      response.end("not found");
    }
  });

  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address() as AddressInfo;
  return {
    baseUrl: `http://127.0.0.1:${port}`,
    close: () =>
      new Promise<void>((resolve, reject) =>
        server.close((error) => (error ? reject(error) : resolve())),
      ),
  };
}

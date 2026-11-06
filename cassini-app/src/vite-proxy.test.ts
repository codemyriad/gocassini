import { createServer as createHTTPServer, request as httpRequest } from "node:http";
import type { AddressInfo } from "node:net";
import { createServer } from "vite";
import { describe, expect, it } from "vitest";
import { createOperatorProxy } from "../vite.config";

// One request per connection. Global fetch keeps connections alive in a pool
// shared by every case, and each case's servers take a fresh port 0; when a
// later case is handed a port an earlier, closed server used, a pooled socket
// to the old server is reused and fails with "other side closed".
function send(url: string, init: { method?: string; body?: string } = {}): Promise<{ status: number; json: () => unknown }> {
  return new Promise((resolve, reject) => {
    const req = httpRequest(url, { method: init.method ?? "GET", agent: false }, (res) => {
      let text = "";
      res.setEncoding("utf8");
      res.on("data", (chunk) => { text += chunk; });
      res.on("end", () => resolve({ status: res.statusCode ?? 0, json: () => JSON.parse(text) }));
    });
    req.on("error", reject);
    req.end(init.body);
  });
}

describe("development operator proxy", () => {
  it.each(["/", "/operator", "/operator.v1"])("forwards the API mounted at %s", async (basePath) => {
    const upstream = createHTTPServer((req, res) => {
      res.setHeader("Content-Type", "application/json");
      res.end(JSON.stringify({ path: req.url, method: req.method }));
    });
    await new Promise<void>((resolve) => upstream.listen(0, "127.0.0.1", resolve));
    const target = `http://127.0.0.1:${(upstream.address() as AddressInfo).port}`;
    const vite = await createServer({
      configFile: false,
      appType: "custom",
      optimizeDeps: { noDiscovery: true, include: [] },
      server: {
        host: "127.0.0.1",
        port: 0,
        proxy: createOperatorProxy(basePath, target),
      },
    });
    try {
      await vite.listen();
      const origin = `http://127.0.0.1:${(vite.httpServer!.address() as AddressInfo).port}`;
      const prefix = basePath === "/" ? "" : basePath;
      for (const route of ["/status", "/readiness", "/setup", "/jobs?limit=1", "/settings/llm", "/settings/workflows", "/storage", "/ai/providers", "/talk/provisioning"]) {
        const path = prefix + route;
        const response = await send(origin + path);
        expect(response.json()).toEqual({ path, method: "GET" });
      }
      for (const [route, method] of [["/readiness/check", "POST"], ["/talk/setup", "PUT"], ["/shares/accept", "POST"], ["/support-bundle", "POST"]]) {
        const path = prefix + route;
        const response = await send(origin + path, { method, body: "{}" });
        expect(response.json()).toEqual({ path, method });
      }
      // These APIs are siblings of the operator prefix in both deployments.
      for (const path of ["/insights", "/insights?limit=1", "/annotations/batch", "/support/bundle"]) {
        const response = await send(origin + path, { method: "POST" });
        expect(response.json()).toEqual({ path, method: "POST" });
      }
      for (const path of ["/settings-panel-missing.js", "/insights-panel-missing.js", "/annotations-panel-missing.js", "/shares-panel-missing.js", "/operator-panel-missing.js", "/operator.v1-panel-missing.js", "/operatorXv1/status"]) {
        const asset = await send(origin + path);
        expect(asset.status, path).toBe(404);
      }
    } finally {
      await vite.close();
      await new Promise<void>((resolve, reject) => upstream.close((err) => err ? reject(err) : resolve()));
    }
  });
});

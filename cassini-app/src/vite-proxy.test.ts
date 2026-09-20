import { createServer as createHTTPServer } from "node:http";
import type { AddressInfo } from "node:net";
import { createServer } from "vite";
import { describe, expect, it } from "vitest";
import { createOperatorProxy } from "../vite.config";

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
        const response = await fetch(origin + path);
        expect(await response.json()).toEqual({ path, method: "GET" });
      }
      for (const [route, method] of [["/readiness/check", "POST"], ["/talk/setup", "PUT"]]) {
        const path = prefix + route;
        const response = await fetch(origin + path, { method, body: "{}" });
        expect(await response.json()).toEqual({ path, method });
      }
      // These APIs are siblings of the operator prefix in both deployments.
      for (const path of ["/insights", "/insights?limit=1", "/annotations/batch"]) {
        const response = await fetch(origin + path, { method: "POST" });
        expect(await response.json()).toEqual({ path, method: "POST" });
      }
      for (const path of ["/settings-panel-missing.js", "/insights-panel-missing.js", "/annotations-panel-missing.js", "/operator-panel-missing.js", "/operator.v1-panel-missing.js", "/operatorXv1/status"]) {
        const asset = await fetch(origin + path);
        expect(asset.status, path).toBe(404);
      }
    } finally {
      await vite.close();
      await new Promise<void>((resolve, reject) => upstream.close((err) => err ? reject(err) : resolve()));
    }
  });
});

import { createServer as createHTTPServer } from "node:http";
import type { AddressInfo } from "node:net";
import { createServer } from "vite";
import { describe, expect, it } from "vitest";
import { createOperatorProxy } from "../vite.config";

describe("development operator proxy", () => {
  it.each(["/", "/operator"])("forwards the API mounted at %s", async (basePath) => {
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
      for (const route of ["/status", "/setup", "/jobs?limit=1", "/settings/llm", "/settings/workflows", "/storage", "/ai/providers", "/talk/provisioning"]) {
        const path = prefix + route;
        const response = await fetch(origin + path);
        expect(await response.json()).toEqual({ path, method: "GET" });
      }
      // These APIs are siblings of the operator prefix in both deployments.
      for (const path of ["/insights", "/annotations/batch"]) {
        const response = await fetch(origin + path, { method: "POST" });
        expect(await response.json()).toEqual({ path, method: "POST" });
      }
      const asset = await fetch(origin + "/settings-panel-missing.js");
      expect(asset.status).toBe(404);
    } finally {
      await vite.close();
      await new Promise<void>((resolve, reject) => upstream.close((err) => err ? reject(err) : resolve()));
    }
  });
});

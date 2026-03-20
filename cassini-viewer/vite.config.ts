import fs from "node:fs";
import path from "node:path";

import { defineConfig, type Plugin } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";

const viewerDemoRoot = path.resolve(__dirname, "exports/viewer-demo");

function serveViewerDemoAssets(): Plugin {
  return {
    name: "cassini-viewer-demo-assets",
    configureServer(server) {
      server.middlewares.use((req, res, next) => {
        const url = req.url ?? "/";
        const pathname = url.split("?")[0] ?? "/";
        if (pathname !== "/catalog.json" && !pathname.startsWith("/meetings/")) {
          next();
          return;
        }

        const relativePath = pathname.replace(/^\/+/, "");
        const filePath = path.resolve(viewerDemoRoot, relativePath);
        if (!filePath.startsWith(viewerDemoRoot + path.sep) && filePath !== viewerDemoRoot) {
          res.statusCode = 403;
          res.end("Forbidden");
          return;
        }

        let stat: fs.Stats;
        try {
          stat = fs.statSync(filePath);
        } catch {
          next();
          return;
        }
        if (!stat.isFile()) {
          next();
          return;
        }

        res.setHeader("Content-Type", contentTypeFor(filePath));
        fs.createReadStream(filePath).pipe(res);
      });
    },
  };
}

function contentTypeFor(filePath: string): string {
  switch (path.extname(filePath).toLowerCase()) {
    case ".json":
      return "application/json; charset=utf-8";
    case ".opus":
      return "audio/ogg";
    case ".vtt":
      return "text/vtt; charset=utf-8";
    case ".txt":
      return "text/plain; charset=utf-8";
    default:
      return "application/octet-stream";
  }
}

export default defineConfig({
  plugins: [svelte(), serveViewerDemoAssets()],
  base: "./",
});

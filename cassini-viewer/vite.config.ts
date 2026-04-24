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
          // NOTE: returning 404 when an artifact is missing (e.g. summary.md) so that:
          // - the load function knows the summary isn't there - default to `null`
          // - the dev server doesn't fall back to HTML (probably serving svelte app with fallback path)
          // TODO: This is noisy and should probably be handled in a different way, but out of scope for now
          res.statusCode = 404;
          res.end("Not Found");
          return;
        }
        if (!stat.isFile()) {
          // NOTE: returning 404 when an artifact is missing (e.g. summary.md) so that:
          // - the load function knows the summary isn't there - default to `null`
          // - the dev server doesn't fall back to HTML (probably serving svelte app with fallback path)
          // TODO: This is noisy and should probably be handled in a different way, but out of scope for now
          res.statusCode = 404;
          res.end("Not Found");
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

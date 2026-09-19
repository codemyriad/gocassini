#!/usr/bin/env node
// Two origins on localhost, so the embed can be tried the way it actually ships
// (D-775).
//
//   npm run build:public
//   node scripts/serve-embed-demo.mjs --recording ../path/to/meeting.opus
//
// Port 4178 stands in for gocassini.com: it serves dist/public as /embed/ and
// the recording as /recording.opus, with Access-Control-Allow-Origin.
// Port 4179 stands in for somebody else's site: it serves test/host.html.
// Deliberately outside Vite's 5173+ range, which a dev server is usually
// already sitting on; override with --asset-port / --host-port.
//
// Two ports rather than one because same-origin hides the bug that matters. A
// plain <script src> crosses origins with no CORS at all; the fetch it then
// makes for the recording does not, and that asymmetry is invisible until the
// page and the recording are actually apart.

import { createServer } from "node:http";
import { createReadStream, existsSync, readFileSync, statSync } from "node:fs";
import { extname, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(here, "..");
const builtDir = join(viewerDir, "dist", "public");

function argValue(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? (process.argv[index + 1] ?? fallback) : fallback;
}

const recording = resolve(process.cwd(), argValue("--recording", ""));
const assetPort = Number(argValue("--asset-port", "4178"));
const hostPort = Number(argValue("--host-port", "4179"));

if (!argValue("--recording", "")) {
  console.error("serve-embed-demo: --recording <file.opus> is required.");
  console.error("  Any Cassini portable .opus. Tag one first with:");
  console.error("    cassini annotate apply <file.opus> --ops ops.json --actor-id <you>");
  process.exit(2);
}
for (const [what, path] of [["the recording", recording], ["dist/public", builtDir]]) {
  if (!existsSync(path)) {
    console.error(`serve-embed-demo: ${what} is missing at ${path}.`);
    if (path === builtDir) console.error("  Run `npm run build:public` first.");
    process.exit(1);
  }
}

const TYPES = {
  ".html": "text/html; charset=utf-8",
  ".js": "application/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".opus": "audio/ogg",
  ".json": "application/json; charset=utf-8",
};

// Range matters here: the viewer asks for the first bytes to read the manifest
// and falls back to the whole file on a 200. Serving both correctly is what
// lets this demo tell you which one your real host will do.
function sendFile(req, res, path, cors) {
  const size = statSync(path).size;
  const headers = { "Content-Type": TYPES[extname(path)] ?? "application/octet-stream" };
  if (cors) {
    headers["Access-Control-Allow-Origin"] = "*";
    headers["Access-Control-Expose-Headers"] = "Content-Range, Content-Length";
  }
  const range = /^bytes=(\d*)-(\d*)$/.exec(req.headers.range ?? "");
  if (range) {
    const start = range[1] ? Number(range[1]) : 0;
    const end = range[2] ? Math.min(Number(range[2]), size - 1) : size - 1;
    res.writeHead(206, {
      ...headers,
      "Content-Range": `bytes ${start}-${end}/${size}`,
      "Content-Length": String(end - start + 1),
      "Accept-Ranges": "bytes",
    });
    createReadStream(path, { start, end }).pipe(res);
    return;
  }
  res.writeHead(200, { ...headers, "Content-Length": String(size), "Accept-Ranges": "bytes" });
  createReadStream(path).pipe(res);
}

function serve(port, route, cors, label) {
  const server = createServer((req, res) => {
    const pathname = (req.url ?? "/").split("?")[0];
    const path = route(pathname);
    if (!path || !existsSync(path) || !statSync(path).isFile()) {
      res.writeHead(404, cors ? { "Access-Control-Allow-Origin": "*" } : {});
      res.end("Not found");
      return;
    }
    sendFile(req, res, path, cors);
  });
  server.on("error", (error) => {
    if (error.code === "EADDRINUSE") {
      console.error(`serve-embed-demo: port ${port} is already in use. Pass --asset-port / --host-port.`);
      process.exit(1);
    }
    throw error;
  });
  server.listen(port, () => console.log(`${label}  http://localhost:${port}`));
}

serve(
  assetPort,
  (pathname) => {
    if (pathname === "/recording.opus") return recording;
    if (pathname.startsWith("/embed/")) return join(builtDir, pathname.slice("/embed/".length));
    return null;
  },
  true,
  "gocassini.com stand-in (embed + recording, CORS on) →",
);

// host.html is templated rather than served flat: it has to name the OTHER
// origin absolutely, and a static file cannot know which port that is.
createServer((req, res) => {
  const pathname = (req.url ?? "/").split("?")[0];
  const path = join(viewerDir, "test", pathname === "/" ? "host.html" : pathname.slice(1));
  if (!existsSync(path) || !statSync(path).isFile()) {
    res.writeHead(404);
    res.end("Not found");
    return;
  }
  if (path.endsWith(".html")) {
    const html = readFileSync(path, "utf8").replaceAll("{{ASSET_ORIGIN}}", `http://localhost:${assetPort}`);
    res.writeHead(200, { "Content-Type": TYPES[".html"] });
    res.end(html);
    return;
  }
  sendFile(req, res, path, false);
}).listen(hostPort, () =>
  console.log(`somebody else's site                            →  http://localhost:${hostPort}`),
);

console.log(`\nOpen http://localhost:${hostPort}/ — the embed is loaded cross-origin from ${assetPort}.`);
console.log(`Recording: ${recording}`);

#!/usr/bin/env node
// Builds the three source-capture bundles into dist/capture/.
//
// These are deliberately NOT part of the Vite embedded build. They are not
// Svelte, they carry no stylesheet, and each has to be a standalone classic
// script with its own global entry point:
//
//   capture-sw.js      a service worker, registered as a classic script
//   capture-worker.js  a dedicated worker (encoded transform + OPFS)
//   capture-payload.js appended to Talk's own bundle by the service worker
//
// esbuild directly is the honest tool for that: three independent IIFEs with no
// shared chunk, which is exactly what Vite's lib mode refuses to emit in one
// pass. The operator serves all three from <dist>/capture/ (see the ExApp's
// uiAssetHandler), so anything that changes a file name here has to change
// there too.

import { build } from "esbuild";
import { readFile } from "node:fs/promises";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");

// The payload is built FIRST and inlined into the service worker. A worker that
// fetched it separately could get a 200 with a truncated body, and a truncated
// payload appended to Talk's bundle is a syntax error in Talk's own script —
// which takes the call page down. Inlining removes the failure mode rather than
// trying to detect it.
const ENTRIES = [
  { in: "src/capture/payload.ts", out: "capture-payload" },
  { in: "src/capture/sw.ts", out: "capture-sw" },
  { in: "src/capture/worker.ts", out: "capture-worker" },
];

let payloadSource = "";

for (const entry of ENTRIES) {
  await build({
    absWorkingDir: root,
    entryPoints: [entry.in],
    outfile: join("dist", "capture", `${entry.out}.js`),
    bundle: true,
    define: {
      __CASSINI_PAYLOAD__: JSON.stringify(payloadSource),
    },
    // Classic scripts, not modules: a service worker registered without
    // {type:"module"} and a Worker constructed the same way both need one
    // self-contained non-module file, and the payload is concatenated onto
    // Talk's bundle where a top-level import would be a syntax error.
    format: "iife",
    platform: "browser",
    target: ["chrome111", "firefox117", "safari16"],
    minify: true,
    sourcemap: false,
    legalComments: "none",
  });
  if (entry.out === "capture-payload") {
    payloadSource = await readFile(join(root, "dist", "capture", "capture-payload.js"), "utf8");
    if (payloadSource.trim() === "") {
      throw new Error("build-capture: the payload bundle is empty; refusing to inline nothing");
    }
  }
  console.log(`build-capture: dist/capture/${entry.out}.js`);
}

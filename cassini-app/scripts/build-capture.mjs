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
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const root = join(here, "..");

const ENTRIES = [
  { in: "src/capture/sw.ts", out: "capture-sw" },
  { in: "src/capture/worker.ts", out: "capture-worker" },
  { in: "src/capture/payload.ts", out: "capture-payload" },
];

for (const entry of ENTRIES) {
  await build({
    absWorkingDir: root,
    entryPoints: [entry.in],
    outfile: join("dist", "capture", `${entry.out}.js`),
    bundle: true,
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
  console.log(`build-capture: dist/capture/${entry.out}.js`);
}

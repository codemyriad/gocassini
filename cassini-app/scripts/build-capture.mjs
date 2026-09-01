#!/usr/bin/env node
// Builds the two source-capture bundles into dist/capture/.
//
// These are deliberately NOT part of the Vite embedded build. They are not
// Svelte, they carry no stylesheet, and each has to be a standalone classic
// script with its own global entry point:
//
//   capture-worker.js  a dedicated worker (encoded transform + OPFS)
//   capture-payload.js an ordinary script injected by the companion PHP app
//
// esbuild directly is the honest tool for that: two independent IIFEs with no
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
  { in: "src/capture/payload.ts", out: "capture-payload" },
  { in: "src/capture/worker.ts", out: "capture-worker" },
];

for (const entry of ENTRIES) {
  await build({
    absWorkingDir: root,
    entryPoints: [entry.in],
    outfile: join("dist", "capture", `${entry.out}.js`),
    bundle: true,
    // Classic scripts, not modules: the companion app loads the payload as an
    // ordinary Nextcloud script and Worker() loads the timing worker without
    // {type:"module"}. Both therefore need a self-contained IIFE.
    format: "iife",
    platform: "browser",
    target: ["chrome111", "firefox117", "safari16"],
    minify: true,
    sourcemap: false,
    legalComments: "none",
  });
  console.log(`build-capture: dist/capture/${entry.out}.js`);
}

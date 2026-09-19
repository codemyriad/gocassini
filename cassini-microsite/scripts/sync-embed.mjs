#!/usr/bin/env node
// Build the public embed and put it where the microsite publishes it (D-775).
//
// The built pair lands in cassini-microsite/public/embed/v1/ and is COMMITTED.
// That keeps the viewer build out of the microsite's deploy path: if compiling
// the viewer broke, it would take the whole site down rather than just the
// embed. Committed assets are served as-is, because Astro copies public/
// verbatim.
//
// The cost of committing build output is drift, so CI runs this script with
// --check and fails if the tree disagrees with a fresh build.
//
// Follow-up: now that .github/workflows/microsite.yml deploys the site, this
// could become a build-time step there.

import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, readdirSync, rmSync, copyFileSync, existsSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const micrositeDir = resolve(here, "..");
const repoRoot = resolve(micrositeDir, "..");
const viewerDir = join(repoRoot, "cassini-viewer");
const builtDir = join(viewerDir, "dist", "public");

// The contract channel. A pasted snippet points here and picks up every
// compatible release; a breaking change to the attribute contract ships as
// /embed/v2/ beside it, leaving v1 answering. Exact-version pins
// (/embed/v1.2.3/) are additive and can be introduced later without moving
// this, which is why they are not here yet.
const CHANNEL = "v1";
const publishedDir = join(micrositeDir, "public", "embed", CHANNEL);
const FILES = ["viewer.js", "viewer.css"];

const check = process.argv.includes("--check");

execFileSync("npm", ["run", "build:public"], { cwd: viewerDir, stdio: "inherit" });

for (const file of FILES) {
  if (!existsSync(join(builtDir, file))) {
    console.error(`sync-embed: ${file} is missing from ${builtDir}`);
    process.exit(1);
  }
}

if (check) {
  let drifted = false;
  for (const file of FILES) {
    const built = readFileSync(join(builtDir, file));
    const published = existsSync(join(publishedDir, file))
      ? readFileSync(join(publishedDir, file))
      : null;
    if (!published || !built.equals(published)) {
      console.error(`sync-embed: public/embed/${CHANNEL}/${file} is not what the source builds.`);
      drifted = true;
    }
  }
  if (drifted) {
    console.error("sync-embed: run `node cassini-microsite/scripts/sync-embed.mjs` and commit the result.");
    process.exit(1);
  }
  console.log(`sync-embed: OK — public/embed/${CHANNEL}/ matches the source.`);
  process.exit(0);
}

rmSync(publishedDir, { recursive: true, force: true });
mkdirSync(publishedDir, { recursive: true });
for (const file of FILES) {
  copyFileSync(join(builtDir, file), join(publishedDir, file));
  console.log(`embed -> public/embed/${CHANNEL}/${file}`);
}

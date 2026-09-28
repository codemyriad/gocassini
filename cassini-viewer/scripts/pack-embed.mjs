#!/usr/bin/env node
// Pack the public embed as a release asset (D-838).
//
//   node scripts/pack-embed.mjs --version 0.2.0 --out build/artifacts/embed
//
// Builds the pair (npm run build:public, which asserts the single-bundle shape)
// and writes cassini-embed-<version>.tar.gz holding viewer.js, viewer.css and
// SHA256SUMS, plus a .sha256 beside it. The release workflow attaches both to
// the GitHub release. That tarball is the embed's published build. Whoever
// hosts the embed serves its two files together from one directory: that is
// https://dist.gocassini.com/embed/<version>/, or anyone self-hosting it
// (ATTRIBUTES.md, "What you must serve").
//
// --no-build packs whatever dist/public already holds.

import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFileSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const viewerDir = resolve(here, "..");
const builtDir = join(viewerDir, "dist", "public");
const FILES = ["viewer.js", "viewer.css"];

function argValue(name) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? process.argv[index + 1] : undefined;
}

const version = argValue("--version");
const out = argValue("--out");
if (!version || !out || !/^[0-9A-Za-z][0-9A-Za-z.-]*$/.test(version)) {
  console.error("usage: pack-embed.mjs --version <version> --out <dir> [--no-build]");
  process.exit(2);
}

if (!process.argv.includes("--no-build")) {
  execFileSync("npm", ["run", "build:public"], { cwd: viewerDir, stdio: "inherit" });
}

const sha256 = (data) => createHash("sha256").update(data).digest("hex");
const staging = mkdtempSync(join(tmpdir(), "cassini-embed-"));
try {
  const sums = FILES.map((file) => {
    copyFileSync(join(builtDir, file), join(staging, file));
    return `${sha256(readFileSync(join(staging, file)))}  ${file}\n`;
  });
  writeFileSync(join(staging, "SHA256SUMS"), sums.join(""));

  const outDir = resolve(process.cwd(), out);
  mkdirSync(outDir, { recursive: true });
  const name = `cassini-embed-${version}.tar.gz`;
  const tarball = join(outDir, name);
  // Fixed order, owner and mtime, so the same build packs to the same bytes.
  execFileSync("tar", [
    "--sort=name", "--mtime=@0", "--owner=0", "--group=0", "--numeric-owner",
    "-I", "gzip -n", "-cf", tarball, "-C", staging, ...FILES, "SHA256SUMS",
  ]);
  writeFileSync(`${tarball}.sha256`, `${sha256(readFileSync(tarball))}  ${name}\n`);

  // Read it back: the asset is only useful if it unpacks to a pair that matches
  // its own checksums.
  const check = mkdtempSync(join(tmpdir(), "cassini-embed-check-"));
  try {
    execFileSync("tar", ["-xzf", tarball, "-C", check]);
    execFileSync("sha256sum", ["--check", "--strict", "SHA256SUMS"], { cwd: check, stdio: "inherit" });
  } finally {
    rmSync(check, { recursive: true, force: true });
  }
  console.log(`pack-embed: ${tarball}`);
} finally {
  rmSync(staging, { recursive: true, force: true });
}

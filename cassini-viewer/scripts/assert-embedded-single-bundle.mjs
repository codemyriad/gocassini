#!/usr/bin/env node
// Build-time guard for the embedded viewer bundle (D-381).
//
// AppAPI loads the registered ui/script on a page whose CSP is
// `script-src-elem 'strict-dynamic' 'nonce-…'`. Under strict-dynamic a classic,
// nonce'd <script> runs, but:
//   - ESM `import`/`export` would make the browser refuse the bundle (a module
//     graph the nonce doesn't cover), and
//   - a runtime `import(...)` would try to fetch a chunk strict-dynamic won't
//     trust.
// So the embedded build MUST emit exactly ONE classic IIFE + ONE stylesheet,
// with no top-level import/export and no dynamic import(). This script asserts
// that shape and fails the build otherwise.
//
// The public embed (D-775) needs the same shape for a different reason: an
// embedding page gave us one <script src> and we cannot make it trust a second
// one, and a page with a strict CSP allows this embed by allowing one URL. So
// the check is parameterised rather than copied:
//
//   node scripts/assert-embedded-single-bundle.mjs --dir dist/public \
//     --js viewer.js --css viewer.css
//
// With no arguments it checks the embedded bundle, as it always has.

import { readFileSync, readdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));

function argValue(name, fallback) {
  const index = process.argv.indexOf(name);
  return index >= 0 ? (process.argv[index + 1] ?? fallback) : fallback;
}

const outDir = argValue("--dir", join("dist", "embedded"));
const expectedJs = argValue("--js", "embedded.js");
const expectedCss = argValue("--css", "embedded.css");
const distDir = join(here, "..", outDir);
const label = `assert-single-bundle(${outDir})`;

function fail(message) {
  console.error(`${label}: ${message}`);
  process.exit(1);
}

// Walk the WHOLE dist/embedded tree (not just the top level): a future
// dependency that introduced code-splitting or a worker would emit an extra
// .js under dist/embedded/assets/* which a top-level-only scan would miss while
// still shipping a script AppAPI's strict-dynamic CSP won't trust.
function walk(dir) {
  let found = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      found = found.concat(walk(full));
    } else {
      found.push(full);
    }
  }
  return found;
}

let allFiles;
try {
  allFiles = walk(distDir);
} catch (error) {
  fail(`cannot read ${distDir}: ${error.message}. Did the matching 'vite build --config ...' run?`);
}

const rel = (p) => p.slice(distDir.length + 1);
const jsFiles = allFiles.filter((p) => p.endsWith(".js")).map(rel);
const cssFiles = allFiles.filter((p) => p.endsWith(".css")).map(rel);

if (jsFiles.length !== 1) {
  fail(`expected exactly one .js bundle under ${distDir}, found ${jsFiles.length}: ${jsFiles.join(", ") || "(none)"}`);
}
if (cssFiles.length !== 1) {
  fail(`expected exactly one .css bundle under ${distDir}, found ${cssFiles.length}: ${cssFiles.join(", ") || "(none)"}`);
}
if (jsFiles[0] !== expectedJs) {
  fail(`expected the JS bundle to be ${expectedJs}, found ${jsFiles[0]}`);
}
if (cssFiles[0] !== expectedCss) {
  fail(`expected the CSS bundle to be ${expectedCss}, found ${cssFiles[0]}`);
}

const jsPath = join(distDir, jsFiles[0]);
const source = readFileSync(jsPath, "utf8");

// 1. No dynamic import() — strict-dynamic won't trust the fetched chunk.
if (/\bimport\s*\(/.test(source)) {
  fail(`${jsFiles[0]} contains a dynamic import() — this bundle must inline every chunk`);
}

// 2. No top-level ESM statements. Scan line-starts (after optional whitespace
//    and `;`) for `import ` / `import{` / `export ` / `export{` / `export*` /
//    `export default`. An IIFE bundle has none; their presence means the lib
//    format regressed to ESM, which the strict-dynamic CSP would reject.
const esmStatement =
  /(^|[\n;])\s*(import\s*[*{'"a-zA-Z_$]|export\s*(\{|\*|default\b|const\b|function\b|class\b|let\b|var\b))/;
if (esmStatement.test(source)) {
  fail(`${jsFiles[0]} contains a top-level ESM import/export statement — this bundle must be a classic IIFE`);
}

console.log(
  `${label}: OK — ${jsFiles[0]} + ${cssFiles[0]} (single classic IIFE, no import()/top-level ESM)`,
);

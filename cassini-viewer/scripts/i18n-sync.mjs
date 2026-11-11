#!/usr/bin/env node
// Keeps the translation JSON in step with the English source (docs/translations.md).
//
//   npm run i18n:sync -w cassini-viewer            write the files
//   npm run i18n:sync -w cassini-viewer -- --check exit 1 if a file would change (CI)
//   npm run i18n:sync -w cassini-viewer -- --coverage   print how much of each language is translated
//
// English lives in src/i18n/en/**/index.ts and developers edit it. This script
// writes en/**/index.json from it (Weblate's base file), then gives every other
// language each missing key as "" and drops keys English no longer has.
// Existing translations are kept. Weblate owns the non-English JSON from there.
//
// Runs under `node --experimental-strip-types`: the English files only import
// types, so node can load them without a build.

import fs from "node:fs/promises";
import { register } from "node:module";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

// The English files import each other the way the app's bundler wants
// ("./browse.js" for browse.ts). Node looks for a .js file, so point it at the
// .ts beside it when there is no .js.
register(
  "data:text/javascript," +
    encodeURIComponent(`
      import { existsSync } from "node:fs";
      import { fileURLToPath } from "node:url";
      export async function resolve(specifier, context, next) {
        if (specifier.startsWith(".") && specifier.endsWith(".js") && context.parentURL?.endsWith(".ts")) {
          const ts = new URL(specifier.slice(0, -3) + ".ts", context.parentURL);
          if (!existsSync(fileURLToPath(new URL(specifier, context.parentURL))) && existsSync(fileURLToPath(ts))) {
            return next(ts.href, context);
          }
        }
        return next(specifier, context);
      }
    `),
);

const I18N_DIR = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../src/i18n");
const BASE = "en";
const args = new Set(process.argv.slice(2));
const check = args.has("--check");
const coverage = args.has("--coverage");

const dirs = async (dir) =>
  (await fs.readdir(dir, { withFileTypes: true })).filter((d) => d.isDirectory()).map((d) => d.name).sort();

// Same shape as English, with this language's strings where it has them.
function sync(base, target) {
  if (typeof base === "string") return typeof target === "string" ? target : "";
  const out = {};
  for (const key of Object.keys(base)) out[key] = sync(base[key], target?.[key]);
  return out;
}

function count(base, target) {
  if (typeof base === "string") return { total: 1, done: typeof target === "string" && target !== "" ? 1 : 0 };
  let total = 0;
  let done = 0;
  for (const key of Object.keys(base)) {
    const c = count(base[key], target?.[key]);
    total += c.total;
    done += c.done;
  }
  return { total, done };
}

async function readJson(file) {
  try {
    return JSON.parse(await fs.readFile(file, "utf8"));
  } catch {
    return {};
  }
}

const stale = [];
async function write(file, data) {
  const text = JSON.stringify(data, null, 2) + "\n";
  const current = await fs.readFile(file, "utf8").catch(() => null);
  if (current === text) return;
  if (check) {
    stale.push(path.relative(process.cwd(), file));
    return;
  }
  await fs.mkdir(path.dirname(file), { recursive: true });
  await fs.writeFile(file, text);
  console.log(`updated ${path.relative(process.cwd(), file)}`);
}

const namespaces = await dirs(path.join(I18N_DIR, BASE));
const locales = (await dirs(I18N_DIR)).filter((l) => l !== BASE);
const tally = Object.fromEntries(locales.map((l) => [l, { total: 0, done: 0 }]));

for (const ns of [null, ...namespaces]) {
  const rel = ns ? path.join(ns, "index") : "index";
  const base = (await import(pathToFileURL(path.join(I18N_DIR, BASE, `${rel}.ts`)).href)).default;
  // The root English file carries only its own keys; namespaces have files of their own.
  await write(path.join(I18N_DIR, BASE, `${rel}.json`), base);
  for (const locale of locales) {
    const file = path.join(I18N_DIR, locale, `${rel}.json`);
    const existing = await readJson(file);
    await write(file, sync(base, existing));
    const c = count(base, existing);
    tally[locale].total += c.total;
    tally[locale].done += c.done;
  }
}

if (coverage) {
  for (const [locale, { total, done }] of Object.entries(tally)) {
    console.log(`${locale.padEnd(6)} ${String(done).padStart(4)}/${total}  ${Math.round((done / total) * 100)}%`);
  }
}

if (stale.length > 0) {
  console.error("Translation files are out of step with the English source. Run `npm run i18n:sync -w cassini-viewer` and commit:");
  for (const file of stale) console.error(`  ${file}`);
  process.exit(1);
}

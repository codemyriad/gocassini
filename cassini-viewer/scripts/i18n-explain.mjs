#!/usr/bin/env node
// Send each English key's `//` comment to Weblate as that string's explanation.
//
// The comments above keys in src/i18n/en/**.ts tell a translator where a string
// appears, what a {placeholder} holds and how long it may be. Weblate reads the
// generated JSON, which has nowhere to keep them, so this script copies them
// onto the English source strings, where Weblate shows them beside every
// translation, followed by the comment on the section the key sits in.
//
//   npm run i18n:explain -w cassini-viewer -- --dry-run   # print, send nothing
//   npm run i18n:explain -w cassini-viewer                # update Weblate
//
// Needs a Weblate API token with access to the project: WEBLATE_TOKEN, or the
// file ~/.config/weblate/codemyriad-token. WEBLATE_URL overrides the instance.
// Only explanations that differ are sent, so running it twice changes nothing.

import { readFileSync, existsSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";

const here = dirname(fileURLToPath(import.meta.url));
const EN = join(here, "..", "src", "i18n", "en");
const WEBLATE_URL = (process.env.WEBLATE_URL ?? "https://weblate.codemyriad.io").replace(/\/$/, "");
const PROJECT = "cassini";
// docs/translations.md › Weblate: one component per dictionary file.
const COMPONENTS = [
  { slug: "nextcloud/nextcloud-common", file: join(EN, "index.ts"), skip: ["app", "viewer"] },
  { slug: "nextcloud/nextcloud-viewer", file: join(EN, "viewer", "index.ts") },
  { slug: "nextcloud/nextcloud-app", file: join(EN, "app", "index.ts") },
];
const dryRun = process.argv.includes("--dry-run");

// --- Comments from the English sources --------------------------------------

function parse(file) {
  return ts.createSourceFile(file, readFileSync(file, "utf8"), ts.ScriptTarget.Latest, true);
}

function commentAbove(node, source) {
  const ranges = ts.getLeadingCommentRanges(source.text, node.getFullStart()) ?? [];
  return ranges
    .map((r) => source.text.slice(r.pos, r.end))
    .filter((c) => c.startsWith("//"))
    .map((c) => c.replace(/^\/\/ ?/, ""))
    .join("\n")
    .trim();
}

// The object literal a file exports by default: `const x = {…}; export default x;`
// (optionally `satisfies` / `as`).
function exportedObject(source) {
  let name;
  for (const s of source.statements) if (ts.isExportAssignment(s) && ts.isIdentifier(s.expression)) name = s.expression.text;
  for (const s of source.statements) {
    if (!ts.isVariableStatement(s)) continue;
    for (const d of s.declarationList.declarations) {
      if (ts.isIdentifier(d.name) && d.name.text === name && d.initializer) return unwrap(d.initializer);
    }
  }
  throw new Error(`${source.fileName}: no default-exported object`);
}

function unwrap(node) {
  while (ts.isSatisfiesExpression(node) || ts.isAsExpression(node) || ts.isParenthesizedExpression(node)) node = node.expression;
  return node;
}

// Imports by local name, so `browse,` in en/viewer/index.ts leads to browse.ts.
function imports(source) {
  const out = new Map();
  for (const s of source.statements) {
    if (!ts.isImportDeclaration(s) || !s.importClause?.name || s.importClause.isTypeOnly) continue;
    const spec = s.moduleSpecifier.text;
    if (!spec.startsWith(".")) continue;
    out.set(s.importClause.name.text, resolve(dirname(source.fileName), spec.replace(/\.js$/, ".ts")));
  }
  return out;
}

function propName(p) {
  const n = p.name;
  return ts.isIdentifier(n) || ts.isStringLiteral(n) || ts.isNumericLiteral(n) ? n.text : n.getText();
}

// key path ("browse.meeting_list.empty_title") → explanation
function collect(file, skip = []) {
  const out = new Map();
  const walk = (source, obj, path, inherited) => {
    const refs = imports(source);
    for (const p of obj.properties) {
      if (!ts.isPropertyAssignment(p) && !ts.isShorthandPropertyAssignment(p)) continue;
      const key = propName(p);
      if (!path.length && skip.includes(key)) continue;
      const own = commentAbove(p, source);
      const keyPath = [...path, key];
      if (ts.isShorthandPropertyAssignment(p) || ts.isIdentifier(unwrap(p.initializer))) {
        const target = refs.get(ts.isShorthandPropertyAssignment(p) ? key : unwrap(p.initializer).text);
        if (!target) continue;
        const sub = parse(target);
        walk(sub, exportedObject(sub), keyPath, own || inherited);
        continue;
      }
      const value = unwrap(p.initializer);
      if (ts.isObjectLiteralExpression(value)) walk(source, value, keyPath, own || inherited);
      else if (ts.isStringLiteral(value) || ts.isNoSubstitutionTemplateLiteral(value)) {
        const text = [own, inherited && `Section: ${inherited}`].filter(Boolean).join("\n\n");
        if (text) out.set(keyPath.join("."), text);
      }
    }
  };
  const source = parse(file);
  walk(source, exportedObject(source), [], "");
  return out;
}

// --- Weblate ------------------------------------------------------------------

function token() {
  if (process.env.WEBLATE_TOKEN) return process.env.WEBLATE_TOKEN.trim();
  const file = join(homedir(), ".config", "weblate", "codemyriad-token");
  if (existsSync(file)) return readFileSync(file, "utf8").trim();
  throw new Error("Set WEBLATE_TOKEN (or write ~/.config/weblate/codemyriad-token)");
}

async function api(path, init = {}) {
  const url = path.startsWith("http") ? path : `${WEBLATE_URL}/api/${path}`;
  const res = await fetch(url, {
    ...init,
    headers: { Authorization: `Token ${token()}`, "Content-Type": "application/json", ...init.headers },
  });
  if (!res.ok) throw new Error(`${init.method ?? "GET"} ${url}: ${res.status} ${await res.text()}`);
  return res.json();
}

async function sourceUnits(component) {
  // A component in a category is addressed as "category%2Fcomponent".
  let next = `translations/${PROJECT}/${encodeURIComponent(component)}/en/units/?page_size=1000`;
  const units = [];
  while (next) {
    const page = await api(next);
    units.push(...page.results);
    next = page.next;
  }
  return units;
}

let changed = 0;
let missing = 0;
for (const { slug, file, skip } of COMPONENTS) {
  const notes = collect(file, skip);
  if (dryRun) {
    console.log(`${slug}: ${notes.size} explained strings`);
    for (const [key, text] of notes) console.log(`  ${key}\n    ${text.replace(/\n/g, "\n    ")}`);
    continue;
  }
  const units = await sourceUnits(slug);
  const byKey = new Map(units.map((u) => [u.context || u.note || "", u]));
  for (const [key, text] of notes) {
    const unit = byKey.get(key);
    if (!unit) { missing++; continue; }
    if ((unit.explanation ?? "") === text) continue;
    await api(`units/${unit.id}/`, { method: "PATCH", body: JSON.stringify({ explanation: text }) });
    changed++;
  }
  console.log(`${slug}: ${notes.size} explained strings, ${units.length} in Weblate`);
}
if (!dryRun) {
  console.log(`updated ${changed} explanations`);
  if (missing) console.log(`${missing} keys not found in Weblate (not pulled yet?)`);
}

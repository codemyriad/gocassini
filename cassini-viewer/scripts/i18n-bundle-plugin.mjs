// Vite plugin: keep the dictionaries a bundle carries to what it uses.
//
// - Every language's JSON holds every key, with "" for a string nobody has
//   translated yet, because Weblate wants the full set. The bundle doesn't:
//   mergeTranslations falls back to English for a missing key exactly as for
//   an empty one, so the empty strings are dropped at build time.
// - withoutApp: the public embed shows only the viewing layer, so the shell's
//   `app` namespace (operator, settings, …) is replaced by an empty object.
//   Nothing under cassini-viewer/src reads LL.app.
// - languageFiles: the public embed is downloaded by every reader of every
//   page that embeds a recording, and each reader needs one language at most.
//   So the bundle keeps English only (every other language's modules become
//   the English ones), and each language is emitted beside it as
//   i18n/<lang>.json for src/public.ts to fetch.

import { existsSync, readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const LANGUAGE_JSON = /\/src\/i18n\/(?!en\/)[^/]+\/(?:(?:viewer|app)\/)?index\.json$/;
const APP_NAMESPACE = /\/src\/i18n\/[^/]+\/app\/index\.ts$/;
const LANGUAGE_MODULE = /^(.*\/src\/i18n\/)(?!en\/)[^/]+\/((?:viewer\/|app\/)?index\.ts)$/;

const I18N_DIR = fileURLToPath(new URL("../src/i18n/", import.meta.url));

function dropEmpty(value) {
  if (typeof value === "string") return value === "" ? undefined : value;
  if (value === null || typeof value !== "object") return value;
  const out = {};
  for (const [key, child] of Object.entries(value)) {
    const kept = dropEmpty(child);
    if (kept !== undefined) out[key] = kept;
  }
  return Object.keys(out).length ? out : undefined;
}

// `over` wins wherever it has a string; both sides have had "" dropped already.
function overlay(base, over) {
  if (!base || typeof base !== "object" || !over || typeof over !== "object") return over ?? base;
  const out = { ...base };
  for (const [key, value] of Object.entries(over)) out[key] = overlay(base[key], value);
  return out;
}

const readJson = (path) => (existsSync(path) ? JSON.parse(readFileSync(path, "utf8")) : {});

// Every language Weblate writes, i.e. every one but the English source.
export function fileLanguages(dir = I18N_DIR) {
  return readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && entry.name !== "en" && existsSync(join(dir, entry.name, "index.json")))
    .map((entry) => entry.name)
    .sort();
}

// One language's common + viewer strings, untranslated ones dropped. A
// regional variant has its base language laid underneath (de-DE over de), as
// de-DE/index.ts does in the bundled builds, so the reader fetches one file.
export function languageDictionary(lang, dir = I18N_DIR) {
  const read = (code) =>
    dropEmpty({ ...readJson(join(dir, code, "index.json")), viewer: readJson(join(dir, code, "viewer", "index.json")) }) ?? {};
  const base = lang.split("-")[0];
  return base !== lang && existsSync(join(dir, base, "index.json")) ? overlay(read(base), read(lang)) : read(lang);
}

export function i18nBundle({ withoutApp = false, languageFiles = false, dir = I18N_DIR } = {}) {
  return {
    name: "cassini-i18n-bundle",
    enforce: "pre",
    load(id) {
      const path = id.split("?")[0];
      if (withoutApp && APP_NAMESPACE.test(path)) return "export default {};";
      const module = languageFiles && path.match(LANGUAGE_MODULE);
      // English in its place: loadAllLocales still finds every locale, and
      // a language whose file never arrives reads as English.
      if (module) return `export { default } from ${JSON.stringify(`${module[1]}en/${module[2]}`)};`;
      return null;
    },
    transform(code, id) {
      if (!LANGUAGE_JSON.test(id.split("?")[0])) return null;
      return { code: JSON.stringify(dropEmpty(JSON.parse(code)) ?? {}), map: null };
    },
    generateBundle() {
      if (!languageFiles) return;
      for (const lang of fileLanguages(dir)) {
        this.emitFile({ type: "asset", fileName: `i18n/${lang}.json`, source: JSON.stringify(languageDictionary(lang, dir)) });
      }
    },
  };
}

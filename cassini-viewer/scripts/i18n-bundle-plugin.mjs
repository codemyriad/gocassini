// Vite plugin: keep the dictionaries a bundle carries to what it uses.
//
// - Every language's JSON holds every key, with "" for a string nobody has
//   translated yet, because Weblate wants the full set. The bundle doesn't:
//   mergeTranslations falls back to English for a missing key exactly as for
//   an empty one, so the empty strings are dropped at build time.
// - withoutApp: the public embed shows only the viewing layer, so the shell's
//   `app` namespace (operator, settings, …) is replaced by an empty object.
//   Nothing under cassini-viewer/src reads LL.app.

const LANGUAGE_JSON = /\/src\/i18n\/(?!en\/)[^/]+\/(?:(?:viewer|app)\/)?index\.json$/;
const APP_NAMESPACE = /\/src\/i18n\/[^/]+\/app\/index\.ts$/;

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

export function i18nBundle({ withoutApp = false } = {}) {
  return {
    name: "cassini-i18n-bundle",
    enforce: "pre",
    load(id) {
      if (withoutApp && APP_NAMESPACE.test(id.split("?")[0])) return "export default {};";
      return null;
    },
    transform(code, id) {
      if (!LANGUAGE_JSON.test(id.split("?")[0])) return null;
      return { code: JSON.stringify(dropEmpty(JSON.parse(code)) ?? {}), map: null };
    },
  };
}

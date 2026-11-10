// Weblate writes every key of a language file, with "" for the strings nobody
// has translated yet. mergeTranslations lays a language's JSON over English,
// key by key, so an untranslated string shows in English instead of empty.
//
// Deep on purpose: typesafe-i18n's extendDictionary merges one level only, so a
// section with one translated string would lose every English string beside it.
export function mergeTranslations<T>(base: T, json: unknown): T {
  return merge(base, json) as T;
}

function merge(base: unknown, over: unknown): unknown {
  if (typeof base === "string") return typeof over === "string" && over !== "" ? over : base;
  if (base === null || typeof base !== "object") return base;
  const out: Record<string, unknown> = {};
  const source = (over && typeof over === "object" ? over : {}) as Record<string, unknown>;
  for (const [key, value] of Object.entries(base)) out[key] = merge(value, source[key]);
  return out;
}

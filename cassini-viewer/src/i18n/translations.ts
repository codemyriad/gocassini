// Weblate writes every key of a language file, with "" for the strings nobody
// has translated yet. mergeTranslations lays a language's JSON over English,
// key by key, so an untranslated string shows in English instead of empty.
//
// Deep on purpose: typesafe-i18n's extendDictionary merges one level only, so a
// section with one translated string would lose every English string beside it.
//
// `base` is untyped because the English source types each string as plain
// `string`, while T (generated) types a string with a {placeholder} as a
// template. T is generated from that same English, so the cast is safe.
export function mergeTranslations<T>(base: unknown, json: unknown): T {
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

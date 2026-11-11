import type { Plugin } from "vite";
export function i18nBundle(options?: { withoutApp?: boolean; languageFiles?: boolean; dir?: string }): Plugin;
export function fileLanguages(dir?: string): string[];
export function languageDictionary(lang: string, dir?: string): Record<string, unknown>;

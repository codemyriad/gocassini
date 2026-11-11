import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { afterAll, describe, expect, it } from "vitest";

import { fileLanguages, i18nBundle, languageDictionary } from "./i18n-bundle-plugin.mjs";

const dir = mkdtempSync(join(tmpdir(), "i18n-bundle-"));
const write = (path: string, value: unknown) => {
  mkdirSync(dirname(join(dir, path)), { recursive: true });
  writeFileSync(join(dir, path), JSON.stringify(value));
};
write("en/index.ts", "");
write("de/index.json", { common: { action: { save: "Speichern", cancel: "Abbrechen" } } });
write("de/viewer/index.json", { embed: { badge: "Mit Cassini", other: "Du" } });
write("de/app/index.json", { operator: { title: "Betreiber" } });
write("de-DE/index.json", { common: { action: { save: "", cancel: "Abbrechen (Sie)" } } });
write("de-DE/viewer/index.json", { embed: { badge: "", other: "Sie" } });
write("fr/index.json", { common: { action: { save: "" } } });
write("fr/viewer/index.json", { embed: { badge: "" } });
afterAll(() => rmSync(dir, { recursive: true, force: true }));

type Hooks = {
  load: (id: string) => string | null;
  generateBundle: (this: { emitFile: (file: unknown) => void }) => void;
};

describe("languageFiles", () => {
  it("lists every language but the English source", () => {
    expect(fileLanguages(dir)).toEqual(["de", "de-DE", "fr"]);
  });

  it("merges common and viewer, drops empty strings and leaves app out", () => {
    expect(languageDictionary("de", dir)).toEqual({
      common: { action: { save: "Speichern", cancel: "Abbrechen" } },
      viewer: { embed: { badge: "Mit Cassini", other: "Du" } },
    });
    expect(languageDictionary("fr", dir)).toEqual({});
  });

  it("lays a regional variant over its base language", () => {
    expect(languageDictionary("de-DE", dir)).toEqual({
      common: { action: { save: "Speichern", cancel: "Abbrechen (Sie)" } },
      viewer: { embed: { badge: "Mit Cassini", other: "Sie" } },
    });
  });

  it("replaces every non-English module with the English one", () => {
    const plugin = i18nBundle({ withoutApp: true, languageFiles: true, dir }) as unknown as Hooks;
    expect(plugin.load("/r/src/i18n/de-DE/viewer/index.ts")).toBe(
      'export { default } from "/r/src/i18n/en/viewer/index.ts";',
    );
    expect(plugin.load("/r/src/i18n/fr/index.ts")).toBe('export { default } from "/r/src/i18n/en/index.ts";');
    expect(plugin.load("/r/src/i18n/fr/app/index.ts")).toBe("export default {};");
    expect(plugin.load("/r/src/i18n/en/viewer/index.ts")).toBeNull();
  });

  it("emits one file per language", () => {
    const plugin = i18nBundle({ languageFiles: true, dir }) as unknown as Hooks;
    const files: { fileName: string; source: string }[] = [];
    plugin.generateBundle.call({ emitFile: (file) => files.push(file as { fileName: string; source: string }) });
    expect(files.map((f) => f.fileName)).toEqual(["i18n/de.json", "i18n/de-DE.json", "i18n/fr.json"]);
    expect(JSON.parse(files[1].source).viewer.embed.other).toBe("Sie");
  });

  it("leaves the other builds bundling every language", () => {
    const plugin = i18nBundle({ dir }) as unknown as Hooks;
    expect(plugin.load("/r/src/i18n/de/viewer/index.ts")).toBeNull();
    const files: unknown[] = [];
    plugin.generateBundle.call({ emitFile: (file) => files.push(file) });
    expect(files).toEqual([]);
  });
});

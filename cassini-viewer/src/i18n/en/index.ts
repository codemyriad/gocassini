import type { BaseTranslation } from "../i18n-types.js";

// English is the source: edit these files, then run `npm run i18n:sync -w
// cassini-viewer`. Every other language is a JSON file that Weblate writes
// (docs/translations.md).
//
// `common` holds words both the shell and the viewing layer use. A string goes
// here only once a second surface needs it.
const en = {
  common: {
    help_translate: "Help translate Cassini",
  },
} satisfies BaseTranslation;

export default en;

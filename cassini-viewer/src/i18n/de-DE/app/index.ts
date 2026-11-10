import type { NamespaceAppTranslation } from "../../i18n-types.js";
import { mergeTranslations } from "../../translations.js";
import de from "../../de/app/index.js";
import json from "./index.json";

// Formal German: falls back to the informal German, then English.
export default mergeTranslations<NamespaceAppTranslation>(de, json);

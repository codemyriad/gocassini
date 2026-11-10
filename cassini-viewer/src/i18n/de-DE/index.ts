import type { Translation } from "../i18n-types.js";
import { mergeTranslations } from "../translations.js";
import de from "../de/index.js";
import json from "./index.json";

// Formal German ("Sie"). A string not yet translated here falls back to the
// informal German, then English, so a reader keeps German where it exists.
export default mergeTranslations<Translation>(de, json);

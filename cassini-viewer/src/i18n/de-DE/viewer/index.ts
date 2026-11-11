import type { NamespaceViewerTranslation } from "../../i18n-types.js";
import { mergeTranslations } from "../../translations.js";
import de from "../../de/viewer/index.js";
import json from "./index.json";

// Formal German: falls back to the informal German, then English.
export default mergeTranslations<NamespaceViewerTranslation>(de, json);

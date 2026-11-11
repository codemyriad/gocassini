import type { NamespaceViewerTranslation } from "../../i18n-types.js";
import { mergeTranslations } from "../../translations.js";
import en from "../../en/viewer/index.js";
import json from "./index.json";

export default mergeTranslations<NamespaceViewerTranslation>(en, json);

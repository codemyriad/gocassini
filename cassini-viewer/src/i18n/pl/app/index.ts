import type { NamespaceAppTranslation } from "../../i18n-types.js";
import { mergeTranslations } from "../../translations.js";
import en from "../../en/app/index.js";
import json from "./index.json";

export default mergeTranslations<NamespaceAppTranslation>(en, json);

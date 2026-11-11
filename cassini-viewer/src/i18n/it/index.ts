import type { Translation } from "../i18n-types.js";
import { mergeTranslations } from "../translations.js";
import en from "../en/index.js";
import json from "./index.json";

export default mergeTranslations<Translation>(en, json);

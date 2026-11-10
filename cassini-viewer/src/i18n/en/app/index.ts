import type { BaseTranslation } from "../../i18n-types.js";
import insights from "./insights.js";
import operator from "./operator.js";
import settings from "./settings.js";
import shell from "./shell.js";

// The Nextcloud shell (cassini-app): navigation, settings and the operator
// surface.
const en_app = {
  shell,
  operator,
  settings,
  insights,
} satisfies BaseTranslation;

export default en_app;

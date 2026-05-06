export interface PanelConfig {
  operatorBasePath: string;
}

export function loadConfig(): PanelConfig {
  const operatorBasePath = normalizeOperatorBasePath(__CASSINI_OPERATOR_BASE_PATH__);
  return { operatorBasePath };
}

function normalizeOperatorBasePath(value: string): string {
  const trimmed = value.trim();
  const normalized = (trimmed === "" ? "/operator" : trimmed).replace(/\/+$/, "") || "/";
  if (!normalized.startsWith("/")) {
    throw new Error("CASSINI_OPERATOR_BASE_PATH must be a root-relative path such as /operator.");
  }
  return normalized;
}

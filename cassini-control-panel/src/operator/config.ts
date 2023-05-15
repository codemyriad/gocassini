export interface PanelConfig {
  operatorBasePath: string;
}

export function loadConfig(): PanelConfig {
  const runtimeValue = window.__CASSINI_CONFIG__?.operatorBasePath;
  const operatorBasePath = normalizeOperatorBasePath(runtimeValue ?? __CASSINI_OPERATOR_BASE_PATH__);
  return { operatorBasePath };
}

function normalizeOperatorBasePath(value: string): string {
  const trimmed = value.trim();
  const normalized = (trimmed === "" ? "/" : trimmed).replace(/\/+$/, "") || "/";
  if (!normalized.startsWith("/")) {
    throw new Error("CASSINI_OPERATOR_BASE_PATH must be a root-relative path such as /operator.");
  }
  return normalized;
}

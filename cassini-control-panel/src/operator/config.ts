export interface PanelConfig {
  operatorUrl: string;
}

export function loadConfig(): PanelConfig {
  const operatorUrl = __CASSINI_OPERATOR_URL__.trim();
  if (operatorUrl === "") {
    throw new Error("Missing CASSINI_OPERATOR_URL. Start the control panel with that environment variable set.");
  }
  return { operatorUrl: operatorUrl.replace(/\/+$/, "") };
}

import { describe, expect, it } from "vitest";

import llmSettingsPanelSource from "./LLMSettingsPanel.svelte?raw";

describe("LLMSettingsPanel key handling", () => {
  it("takes keys through a password input and never renders a stored key", () => {
    expect(llmSettingsPanelSource).toContain('type="password"');
    // The server only ever reports api_key_configured; the panel must not
    // expect or bind a raw key field.
    expect(llmSettingsPanelSource).toContain("keyConfigured");
    expect(llmSettingsPanelSource).not.toContain("api_key_value");
  });

  it("offers fetched model lists while keeping the model field free text", () => {
    expect(llmSettingsPanelSource).toContain("Load models");
    expect(llmSettingsPanelSource).toContain("<datalist");
    expect(llmSettingsPanelSource).toContain('type="text"');
  });
});

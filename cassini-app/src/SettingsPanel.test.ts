import { describe, expect, it } from "vitest";

import settingsPanelSource from "./SettingsPanel.svelte?raw";

describe("SettingsPanel device policy", () => {
  it("offers auto and CUDA without exposing the unsupported CPU override", () => {
    expect(settingsPanelSource).toContain('<option value="">Auto</option>');
    expect(settingsPanelSource).toContain('<option value="cuda">CUDA</option>');
    expect(settingsPanelSource).not.toContain('<option value="cpu">');
  });
});

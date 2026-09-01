import { describe, expect, it } from "vitest";

import settingsPanelSource from "./SettingsPanel.svelte?raw";

describe("SettingsPanel device policy", () => {
  it("offers auto, CPU and CUDA as device overrides, and no model override", () => {
    expect(settingsPanelSource).toContain('<option value="">Auto</option>');
    expect(settingsPanelSource).toContain('<option value="cpu">CPU</option>');
    expect(settingsPanelSource).toContain('<option value="cuda">CUDA</option>');
    // The model override accepted only the three models the quality tiers
    // already reach, so it duplicated the tier selector with no discoverability
    // and was removed (D-702).
    expect(settingsPanelSource).not.toContain("modelOverride");
  });

  it("shows the device and model the next build will actually use", () => {
    // The device is auto-selected, so the tier alone does not tell an admin
    // what will happen. Without this an install with no GPU looks configured
    // and only reveals the CPU path once a build has run (D-702).
    expect(settingsPanelSource).toContain("deviceLabel(settings.effective.device)");
    expect(settingsPanelSource).toContain("settings.effective.model");
    expect(settingsPanelSource).toContain("settings.effective.note");
  });
});

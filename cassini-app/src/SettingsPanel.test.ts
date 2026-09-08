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

describe("SettingsPanel search spellings", () => {
  it("sends the aliases and tracks them as unsaved changes", () => {
    // Without the payload field the control would look like it worked and
    // silently change nothing; without the dirty check Save stays disabled
    // after editing it.
    expect(settingsPanelSource).toContain("search_aliases: parseSearchAliases(searchAliasesText)");
    expect(settingsPanelSource).toContain("searchAliasesText !== savedSearchAliasesText");
  });

  it("says what the setting does and does not do", () => {
    // It widens a search; it never rewrites a recording. An admin who thought
    // this fixed transcripts would use it for the wrong job — the vocabulary
    // above is that one.
    expect(settingsPanelSource).toContain("does not change any recording");
  });
});

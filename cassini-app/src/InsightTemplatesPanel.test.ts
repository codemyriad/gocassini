import { describe, expect, it } from "vitest";

import insightTemplatesPanelSource from "./InsightTemplatesPanel.svelte?raw";

describe("InsightTemplatesPanel empty state", () => {
  it("says the registry does not exist rather than listing templates", () => {
    // The nav row ships ahead of the registry (D-723 / D-718). A panel that
    // showed the design's example templates would read as a set an
    // administrator can already pick from, which is the one thing it must not
    // claim — so the panel renders no list at all.
    expect(insightTemplatesPanelSource).toContain("No templates to show yet.");
    expect(insightTemplatesPanelSource).toContain("no template registry behind this panel yet");
    expect(insightTemplatesPanelSource).not.toContain("{#each");
  });
});

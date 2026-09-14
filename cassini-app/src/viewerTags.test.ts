import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import ColorSwatchPicker from "cassini-viewer/tags/ColorSwatchPicker.svelte";
import TagChip from "cassini-viewer/tags/TagChip.svelte";
import { colorFor } from "cassini-viewer/tagPalette";

describe("the viewer's tag components, from the app", () => {
  it("render through the package exports", () => {
    const color = colorFor({ tagId: "tag_a", color: "violet" });
    expect(render(TagChip, { props: { label: "hiring", color } }).body).toContain('data-tag-color="violet"');
    expect(render(ColorSwatchPicker, { props: { value: color } }).body).toContain('aria-label="Violet"');
  });
});

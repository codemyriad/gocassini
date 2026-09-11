import { describe, expect, it } from "vitest";
import { render } from "svelte/server";

import type { VocabularyTag } from "../../viewer/annotations";
import { leastUsedColor } from "../../viewer/tagPalette";
import ColorSwatchPicker from "./ColorSwatchPicker.svelte";
import swatchSource from "./ColorSwatchPicker.svelte?raw";
import TagChip from "./TagChip.svelte";
import TagIcon from "./TagIcon.svelte";
import TagPicker from "./TagPicker.svelte";
import pickerSource from "./TagPicker.svelte?raw";
import { isOutside, stepIndex } from "./popover";

// Server-rendered, because the suite runs in node with no DOM. Keyboard and
// pointer behaviour lives in popover.ts, which is tested directly.

function tag(tagId: string, label: string, meetings: number, color: VocabularyTag["color"] = ""): VocabularyTag {
  return { tagId, namespace: "ns", label, meetings, marks: meetings, color, icon: "", changedBy: "", changedAtUtc: "" };
}
const tags = [tag("tag_h", "hiring", 4, "teal"), tag("tag_b", "budget", 2), tag("tag_r", "HR", 1)];
const html = (component: unknown, props: Record<string, unknown>) =>
  render(component as never, { props } as never).body;

describe("TagIcon", () => {
  it("draws the icon, or a dot when the tag has none", () => {
    expect(html(TagIcon, { icon: "star" })).toMatch(/<svg[^>]*aria-hidden="true"[^>]*><path d="M12 3l/);
    const dot = html(TagIcon, { icon: "" });
    expect(dot).not.toContain("<svg");
    expect(dot).toContain("tag-dot");
  });
});

describe("TagChip", () => {
  it("always shows the label, in the tag's colour", () => {
    const chip = html(TagChip, { label: "hiring", color: "teal", variant: "whole" });
    expect(chip).toContain('data-tag-color="teal"');
    expect(chip).toMatch(/class="[^"]*\bwhole\b/);
    expect(chip).toContain(">hiring</span>");
  });

  it("counts stretches only when there is more than one", () => {
    // The space is load-bearing: without it a screen reader says "3stretches".
    expect(html(TagChip, { label: "hiring", color: "teal", count: 3 })).toMatch(/>3<span class="sr-only[^"]*"> stretches</);
    expect(html(TagChip, { label: "hiring", color: "teal", count: 1 })).not.toContain("stretches");
  });

  it("names its remove button after the tag", () => {
    expect(html(TagChip, { label: "hiring", color: "teal" })).not.toContain("<button");
    expect(html(TagChip, { label: "hiring", color: "teal", removable: true })).toContain('aria-label="Remove hiring"');
  });
});

describe("ColorSwatchPicker", () => {
  it("offers the twelve colours by name, the current one checked and focusable", () => {
    const grid = html(ColorSwatchPicker, { value: "teal" });
    expect(grid).toContain('role="radiogroup" aria-label="Tag colour"');
    expect(grid.match(/role="radio"/g)).toHaveLength(12);
    expect(grid.match(/aria-checked="true"/g)).toHaveLength(1);
    expect(grid).toMatch(/aria-checked="true" aria-label="Teal"[^>]*tabindex="0"/);
    expect(grid.match(/tabindex="0"/g)).toHaveLength(1);
  });

  it("closes itself alone on Esc and hands focus back", () => {
    expect(swatchSource).toMatch(/"Escape"\) \{\s*event\.preventDefault\(\);\s*event\.stopPropagation\(\);\s*anchor\?\.focus\(\);/);
  });
});

describe("TagPicker", () => {
  it("wires the field to the list as a combobox", () => {
    const picker = html(TagPicker, { tags, label: "Tag this stretch" });
    const listId = picker.match(/<ul id="([^"]+)" role="listbox"/)?.[1];
    expect(picker).toContain('role="dialog" aria-label="Tag this stretch"');
    expect(picker).toContain(`role="combobox" aria-expanded="true" aria-controls="${listId}"`);
    expect(picker).toContain(`aria-activedescendant="${listId?.replace(/-list$/, "")}-0"`);
  });

  it("filters the vocabulary and offers to create what does not exist, in the colour it will get", () => {
    const picker = html(TagPicker, { tags, query: "h" });
    expect(picker).toContain(">HR</span>");
    expect(picker).toContain(">hiring</span>");
    expect(picker).not.toContain(">budget</span>");
    expect(picker).toMatch(/Create <b[^>]*>“h”<\/b>/);
    expect(picker).toContain(`aria-label="Colour: ${leastUsedColor(tags).replace(/^./, (c) => c.toUpperCase())}"`);
  });

  it("does not offer to create a label that already exists in another case", () => {
    expect(html(TagPicker, { tags, query: " Hiring" })).not.toContain("Create");
  });

  it("says so when there are no tags yet", () => {
    expect(html(TagPicker, { tags: [] })).toContain("No tags yet. Type a name to create one.");
  });

  it("ticks the tags already on, for toggling several", () => {
    const picker = html(TagPicker, { tags, multiple: true, selected: ["tag_b"], mixed: ["tag_r"] });
    expect(picker).toContain('aria-multiselectable="true"');
    expect(picker.match(/aria-selected="true"/g)).toHaveLength(1);
    expect(picker).toMatch(/aria-selected="true"[^>]*data-tag-color="[a-z]+"[\s\S]*?class="tick[^"]*\bon\b/);
    expect(picker).toMatch(/class="tick[^"]*\bmixed\b/);
  });

  it("emits an existing tag by id, and a new one with its colour and no icon", () => {
    expect(pickerSource).toContain('dispatch("pick", { tagId: tag.tagId, label: tag.label })');
    expect(pickerSource).toContain('dispatch("pick", { label: draft, color: newColor, icon: "" })');
  });

  it("closes on Esc and hands focus back to what opened it", () => {
    expect(pickerSource).toMatch(/function close\(\) \{\s*anchor\?\.focus\(\);\s*dispatch\("close"\);/);
  });
});

describe("popover keys", () => {
  it("moves through a list with Up and Down, wrapping, and leaves Left and Right to the caret", () => {
    expect(stepIndex(0, "ArrowDown", 3)).toBe(1);
    expect(stepIndex(0, "ArrowUp", 3)).toBe(2);
    expect(stepIndex(2, "ArrowDown", 3)).toBe(0);
    expect(stepIndex(1, "ArrowLeft", 3)).toBeNull();
    expect(stepIndex(0, "ArrowDown", 0)).toBeNull();
  });

  it("moves through a grid by rows and columns", () => {
    expect(stepIndex(1, "ArrowDown", 12, 6)).toBe(7);
    expect(stepIndex(1, "ArrowUp", 12, 6)).toBe(7);
    expect(stepIndex(11, "ArrowRight", 12, 6)).toBe(0);
    expect(stepIndex(0, "Enter", 12, 6)).toBeNull();
  });
});

describe("isOutside", () => {
  it("reads the composed path, which sees through Nextcloud's shadow root", () => {
    const root = {} as Element;
    const anchor = {} as Element;
    const at = (...path: object[]) => ({ composedPath: () => path }) as unknown as Event;
    expect(isOutside(at(root), root, anchor)).toBe(false);
    expect(isOutside(at(anchor), root, anchor)).toBe(false);
    expect(isOutside(at({}), root, null)).toBe(true);
  });
});

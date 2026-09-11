import { describe, expect, it, vi } from "vitest";
import { render } from "svelte/server";

import type { VocabularyTag } from "../../viewer/annotations";
import IconGrid from "./manager/IconGrid.svelte";
import TagEditor from "./manager/TagEditor.svelte";
import TagManager from "./TagManager.svelte";
import TagPicker from "./TagPicker.svelte";

// Server-rendered, as the suite has no DOM. What the manager does with a job
// is tested in viewer/tagManager.test.ts.

function tag(tagId: string, label: string, over: Partial<VocabularyTag> = {}): VocabularyTag {
  return { tagId, namespace: "ns", label, meetings: 9, marks: 14, color: "", icon: "", changedBy: "", changedAtUtc: "", ...over };
}
const tags = [
  tag("tag_b", "budget", { color: "amber", changedBy: "Priya", changedAtUtc: new Date(Date.now() - 2 * 864e5).toISOString() }),
  tag("tag_h", "hiring", { meetings: 1, marks: 0, icon: "star" }),
];
const provider = {
  updateTag: vi.fn(),
  mergeTag: vi.fn(),
  deleteTag: vi.fn(),
  loadTagJob: vi.fn(async () => null),
};
const html = (component: unknown, props: Record<string, unknown>) =>
  render(component as never, { props } as never).body;

describe("TagManager", () => {
  it("is a modal sheet only while open", () => {
    expect(html(TagManager, { tags, provider, open: false })).not.toContain("Manage tags");
    const sheet = html(TagManager, { tags, provider, open: true });
    expect(sheet).toMatch(/role="dialog" aria-modal="true" aria-labelledby="tag-manager-title"/);
    expect(sheet).toContain('aria-label="Close"');
    expect(sheet).toMatch(/2 tags<\/span>/);
  });

  it("lists each tag with its counts and a menu, and who changed it only once someone has", () => {
    const sheet = html(TagManager, { tags, provider, open: true });
    expect(sheet).toContain(">budget</span>");
    expect(sheet).toContain("9 meetings · 14 marks");
    expect(sheet).toContain("1 meeting · 0 marks");
    expect(sheet).toContain("Changed by Priya · 2 days ago");
    expect(sheet.match(/Changed by/g)).toHaveLength(1);
    expect(sheet).toContain('aria-label="Actions for hiring" aria-haspopup="menu"');
    expect(sheet).toMatch(/data-tag-color="amber"/);
  });

  it("says so in one quiet line when there are no tags", () => {
    expect(html(TagManager, { tags: [], provider, open: true })).toContain("No tags yet.");
  });
});

describe("TagEditor", () => {
  it("puts colour, icon and name in one row", () => {
    const editor = html(TagEditor, { tag: tags[0] });
    expect(editor).toContain('aria-label="Colour: Amber"');
    expect(editor).toContain('aria-label="Add an icon"');
    expect(editor).toMatch(/value="budget"[^>]*aria-label="Tag name"|aria-label="Tag name"[^>]*value="budget"/);
    expect(editor).toContain(">Save</button>");
    expect(editor).not.toContain("Updates");
  });

  it("offers a merge when the name belongs to another tag", () => {
    const editor = html(TagEditor, { tag: tags[0], conflict: { tagId: "tag_h", label: "hiring" } });
    expect(editor).toContain("“hiring” already exists.");
    expect(editor).toContain("Merge into “hiring” instead");
  });
});

describe("IconGrid", () => {
  it("offers no icon, the default, and the sixteen icons", () => {
    const grid = html(IconGrid, { value: "", color: "teal" });
    expect(grid.match(/role="radio"/g)).toHaveLength(17);
    expect(grid).toMatch(/role="radio" aria-checked="true"[^>]*>[\s\S]*?No icon/);
    expect(grid.match(/aria-checked="true"/g)).toHaveLength(1);
    expect(html(IconGrid, { value: "star", color: "teal" })).toMatch(/aria-checked="true" aria-label="Star"/);
  });
});

describe("TagPicker without create", () => {
  it("only finds existing tags, as a merge target must", () => {
    const picker = html(TagPicker, { tags, query: "bud", creatable: false });
    expect(picker).toContain(">budget</span>");
    expect(picker).toContain('placeholder="Find a tag"');
    expect(html(TagPicker, { tags, query: "new one", creatable: false })).not.toContain("Create");
  });
});

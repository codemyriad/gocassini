// The tag palette (D-746). The colour values live in app.css as custom
// properties; `data-tag-color="<id>"` on an element sets its --tag, --tag-bg
// and --tag-border.

export const TAG_COLORS = [
  "slate",
  "red",
  "orange",
  "amber",
  "green",
  "teal",
  "cyan",
  "blue",
  "indigo",
  "violet",
  "purple",
  "pink",
] as const;
export type TagColorId = (typeof TAG_COLORS)[number];

// Line icons on a 24×24 grid, drawn with a 2px round stroke and no fill.
export const TAG_ICON_PATHS = {
  star: "M12 3l2.8 5.7 6.2.9-4.5 4.4 1.1 6.2-5.6-2.9-5.6 2.9 1.1-6.2L3 9.6l6.2-.9z",
  flag: "M5 21V4h12l-3 4.5 3 4.5H5",
  bolt: "M13 2 4 14h7l-1 8 9-12h-7z",
  bookmark: "M6 3h12v18l-6-4-6 4z",
  check: "M4 12.5l5 5L20 6.5",
  alert: "M12 3 2 20h20zM12 10v4M12 17h.01",
  bug: "M9 7a3 3 0 0 1 6 0M8 10a4 4 0 0 1 8 0v4a4 4 0 0 1-8 0zM12 12v6M4 13h4M16 13h4M5 9l3 1.5M19 9l-3 1.5M5 18l3-1.5M19 18l-3-1.5",
  heart: "M12 20s-7-4.4-7-10a4 4 0 0 1 7-2.6A4 4 0 0 1 19 10c0 5.6-7 10-7 10z",
  question: "M12 3a9 9 0 1 0 0 18 9 9 0 1 0 0-18zM9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.6.3-1 .9-1 1.6v.6M12 17h.01",
  lightbulb: "M9 18h6M10 21h4M12 3a6 6 0 0 0-3.5 10.9c.3.3.5.7.5 1.1v1h6v-1c0-.4.2-.8.5-1.1A6 6 0 0 0 12 3z",
  target: "M12 3a9 9 0 1 0 0 18 9 9 0 1 0 0-18zM12 7a5 5 0 1 0 0 10 5 5 0 1 0 0-10zM12 11a1 1 0 1 0 0 2 1 1 0 1 0 0-2z",
  users: "M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 1 0 0 7zM3 20a6 6 0 0 1 12 0M16 4.5a3.5 3.5 0 0 1 0 6.5M18 14.5c1.8.8 3 2.8 3 5.5",
  calendar: "M4 6h16v15H4zM4 10h16M8 3v4M16 3v4",
  money: "M12 2v20M17 6.5C16.2 5 14.3 4 12 4 9.2 4 7 5.5 7 7.8 7 13 17 10.5 17 16c0 2.3-2.2 4-5 4-2.4 0-4.4-1-5.2-2.7",
  lock: "M5 11h14v10H5zM8 11V7a4 4 0 0 1 8 0v4",
  chat: "M4 5h16v11H9l-5 4z",
} as const;
export type TagIconId = keyof typeof TAG_ICON_PATHS;
export const TAG_ICONS = Object.keys(TAG_ICON_PATHS) as TagIconId[];

export function isTagColor(value: unknown): value is TagColorId {
  return TAG_COLORS.includes(value as TagColorId);
}

export function colorName(color: TagColorId): string {
  return color.charAt(0).toUpperCase() + color.slice(1);
}

type TagColorSource = { tagId: string; color?: string } | { id: string; color?: string };

// The default comes from the id, never the label, so a rename keeps the colour.
export function colorFor(tag: TagColorSource): TagColorId {
  if (isTagColor(tag.color)) {
    return tag.color;
  }
  const id = "tagId" in tag ? tag.tagId : tag.id;
  let hash = 0x811c9dc5;
  for (let i = 0; i < id.length; i++) {
    hash = Math.imul(hash ^ id.charCodeAt(i), 0x01000193);
  }
  return TAG_COLORS[(hash >>> 0) % TAG_COLORS.length];
}

// Ties go to the earlier colour in the palette.
export function leastUsedColor(tags: readonly TagColorSource[]): TagColorId {
  const uses = new Map<TagColorId, number>(TAG_COLORS.map((color) => [color, 0]));
  for (const tag of tags) {
    const color = colorFor(tag);
    uses.set(color, (uses.get(color) ?? 0) + 1);
  }
  return TAG_COLORS.reduce((best, color) =>
    (uses.get(color) ?? 0) < (uses.get(best) ?? 0) ? color : best,
  );
}

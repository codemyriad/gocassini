import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

import { TAG_COLORS, TAG_ICONS, TAG_ICON_PATHS, colorFor, leastUsedColor } from "./tagPalette";

const appCss = readFileSync(new URL("../app.css", import.meta.url), "utf8");

describe("colorFor", () => {
  it("uses the stored colour when there is one", () => {
    expect(colorFor({ tagId: "tag_a", color: "pink" })).toBe("pink");
  });

  it("derives the default from the id alone, so a rename keeps it", () => {
    const derived = colorFor({ tagId: "tag_k3v9q2m7x4d8w1pz", color: "" });
    expect(colorFor({ id: "tag_k3v9q2m7x4d8w1pz" })).toBe(derived);
    expect(colorFor({ tagId: "tag_k3v9q2m7x4d8w1pz", color: "not-a-colour" })).toBe(derived);
  });

  it("spreads ids across the palette", () => {
    const used = new Set(Array.from({ length: 200 }, (_, i) => colorFor({ id: `tag_${i}` })));
    expect(used.size).toBe(TAG_COLORS.length);
  });
});

describe("leastUsedColor", () => {
  it("picks the colour fewest tags have, earliest in the palette on a tie", () => {
    expect(leastUsedColor([])).toBe("slate");
    expect(leastUsedColor([{ tagId: "a", color: "slate" }, { tagId: "b", color: "red" }])).toBe("orange");
  });
});

describe("icons", () => {
  it("offers the sixteen agreed ids, each with a path", () => {
    expect(TAG_ICONS).toEqual([
      "star", "flag", "bolt", "bookmark", "check", "alert", "bug", "heart",
      "question", "lightbulb", "target", "users", "calendar", "money", "lock", "chat",
    ]);
    for (const icon of TAG_ICONS) {
      expect(TAG_ICON_PATHS[icon]).toMatch(/^M/);
    }
  });
});

// oklch → sRGB, and the WCAG contrast ratio, to check the palette app.css ships.
type Lab = [number, number, number];
const oklab = ([l, c, h]: number[]): Lab => [l, c * Math.cos((h * Math.PI) / 180), c * Math.sin((h * Math.PI) / 180)];
const mix = (a: Lab, b: Lab, p: number): Lab => [0, 1, 2].map((i) => a[i] * p + b[i] * (1 - p)) as Lab;
function linearRgb([L, a, b]: Lab): number[] {
  const l = (L + 0.3963377774 * a + 0.2158037573 * b) ** 3;
  const m = (L - 0.1055613458 * a - 0.0638541728 * b) ** 3;
  const s = (L - 0.0894841775 * a - 1.291485548 * b) ** 3;
  return [
    4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s,
    -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s,
    -0.0041960863 * l - 0.7034186147 * m + 1.707614701 * s,
  ];
}
const luminance = (lab: Lab) => {
  const [r, g, b] = linearRgb(lab).map((v) => Math.min(1, Math.max(0, v)));
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};
const contrast = (x: Lab, y: Lab) => {
  const [hi, lo] = [luminance(x), luminance(y)].sort((p, q) => q - p);
  return (hi + 0.05) / (lo + 0.05);
};
const oklchValues = (property: string) =>
  [...appCss.matchAll(new RegExp(`${property}:\\s*oklch\\(([\\d.]+)% ([\\d.]+) ([\\d.]+)\\)`, "g"))].map(
    (match) => oklab([Number(match[1]) / 100, Number(match[2]), Number(match[3])]),
  );
const percents = (property: string) =>
  [...appCss.matchAll(new RegExp(`${property}:\\s*([\\d.]+)%`, "g"))].map((match) => Number(match[1]) / 100);

describe("the palette in app.css", () => {
  const bases = oklchValues("--color-base-100");
  const tints = percents("--tag-tint-mix");

  it.each(["light", "dark"] as const)("keeps every colour legible in the %s theme", (theme) => {
    const at = theme === "light" ? 0 : 1;
    for (const color of TAG_COLORS) {
      const ink = oklchValues(`--tag-${color}`)[at];
      expect(linearRgb(ink).every((v) => v > -0.001 && v < 1.001), `${color} in sRGB`).toBe(true);
      // Chip text on its own tint, and the ground on a solid chip.
      expect(contrast(ink, mix(ink, bases[at], tints[at])), `${color} on its tint`).toBeGreaterThanOrEqual(4.5);
      expect(contrast(bases[at], ink), `ground on ${color}`).toBeGreaterThanOrEqual(4.5);
    }
  });

  it("gives every colour its data-tag-color rule", () => {
    for (const color of TAG_COLORS) {
      expect(appCss).toContain(`[data-tag-color="${color}"] { --tag: var(--tag-${color}); }`);
    }
  });
});

export interface NavItem {
  label: string;
  slug: string;
}

export interface NavSection {
  section: string;
  items: NavItem[];
}

export const docsNav: NavSection[] = [
  {
    section: "Getting Started",
    items: [
      { label: "Introduction", slug: "getting-started/introduction" },
      { label: "Quick Start", slug: "getting-started/quick-start" },
    ],
  },
  {
    section: "Guides",
    items: [
      { label: "Overview", slug: "guides/overview" },
      { label: "Configuration", slug: "guides/configuration" },
    ],
  },
];

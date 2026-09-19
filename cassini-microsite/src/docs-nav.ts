export interface NavItem {
  label: string;
  /** Slug under /docs. The empty string is the docs index at /docs. */
  slug: string;
}

export interface NavSection {
  section: string;
  items: NavItem[];
}

export const docsNav: NavSection[] = [
  {
    section: "Getting started",
    items: [
      { label: "Introduction", slug: "" },
      { label: "Install on Nextcloud", slug: "getting-started/install" },
    ],
  },
  {
    section: "Guides",
    items: [
      { label: "Who can see a recording", slug: "guides/who-can-see-a-recording" },
      { label: "CPU or GPU", slug: "guides/cpu-or-gpu" },
      { label: "AI providers, summaries and insights", slug: "guides/ai-providers" },
      { label: "The meeting file", slug: "guides/meeting-file" },
      { label: "Agent access via the CLI", slug: "guides/agent-access" },
      { label: "Privacy and data processing", slug: "guides/privacy" },
      { label: "Troubleshooting", slug: "guides/troubleshooting" },
    ],
  },
];

/** Links under the sidebar nav, outside the page order above. */
export const docsNavFooter: NavItem[] = [
  { label: "The meeting file format", slug: "https://format.gocassini.com" },
  { label: "GitHub", slug: "https://github.com/codemyriad/gocassini" },
];

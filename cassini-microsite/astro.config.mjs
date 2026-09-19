import { defineConfig } from "astro/config";
import mdx from "@astrojs/mdx";
import sitemap from "@astrojs/sitemap";
import tailwindcss from "@tailwindcss/vite";

// Shiki closes untagged (plaintext) code blocks with an empty trailing
// `<span class="line">`, which renders as a stray blank line under every such
// block. Language-tagged blocks don't get one, so strip it for parity.
const stripTrailingBlankLine = {
  name: "strip-trailing-blank-line",
  code(node) {
    const text = n =>
      n.type === "text" ? n.value : (n.children ?? []).map(text).join("");
    while (node.children.length) {
      const last = node.children[node.children.length - 1];
      if (last.type === "text" && last.value.trim() === "") {
        node.children.pop();
        continue;
      }
      if (last.type === "element" && text(last) === "") {
        node.children.pop();
        continue;
      }
      break;
    }
  },
};

// GitHub-style alerts (`> [!WARNING]` …) render as daisyUI alert boxes, so the
// same markdown reads as a callout on GitHub and on the site. Other kinds fall
// through as ordinary blockquotes.
const alertKinds = {
  WARNING: {
    className: "alert-warning",
    icon: '<svg xmlns="http://www.w3.org/2000/svg" width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="shrink-0 mt-0.5" aria-hidden="true"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>',
  },
};

function remarkGithubAlerts() {
  const visit = node => {
    for (const child of node.children ?? []) visit(child);
    if (node.type !== "blockquote") return;
    const first = node.children?.[0];
    const text = first?.type === "paragraph" ? first.children?.[0] : null;
    const match = text?.type === "text" && /^\[!(\w+)\]\s*\n?/.exec(text.value);
    const kind = match && alertKinds[match[1].toUpperCase()];
    if (!kind) return;
    text.value = text.value.slice(match[0].length);
    if (!text.value) first.children.shift();
    node.data = {
      hName: "div",
      hProperties: { role: "alert", className: ["alert", kind.className, "not-prose", "mb-8", "items-start"] },
    };
    first.data = { hProperties: { className: ["m-0"] } };
    const styleLinks = n => {
      if (n.type === "link") n.data = { hProperties: { className: ["link"] } };
      for (const child of n.children ?? []) styleLinks(child);
    };
    styleLinks(node);
    node.children.unshift({ type: "html", value: kind.icon });
  };
  return tree => visit(tree);
}

export default defineConfig({
  site: "https://gocassini.com",
  base: process.env.BASE_PATH,
  integrations: [mdx(), sitemap()],
  markdown: {
    remarkPlugins: [remarkGithubAlerts],
    shikiConfig: {
      transformers: [stripTrailingBlankLine],
    },
  },
  vite: {
    plugins: [tailwindcss()],
  },
});

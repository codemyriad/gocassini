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

export default defineConfig({
  site: "https://gocassini.codemyriad.io",
  base: process.env.BASE_PATH,
  integrations: [mdx(), sitemap()],
  markdown: {
    shikiConfig: {
      transformers: [stripTrailingBlankLine],
    },
  },
  vite: {
    plugins: [tailwindcss()],
  },
});

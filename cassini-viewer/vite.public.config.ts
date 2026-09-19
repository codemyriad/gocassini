import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

// Public-embed build (D-775).
//
// Emits ONE classic IIFE (dist/public/viewer.js) + ONE stylesheet
// (dist/public/viewer.css) — the pair published under gocassini.com/embed/v1/
// and /embed/v<semver>/. src/public.ts resolves the stylesheet as a sibling of
// its own script URL, so the two must be published together in one directory
// and nothing about the version is compiled in.
//
// The single-file shape is pinned exactly as vite.embedded.config.ts pins it,
// for a different reason: an embedding page gave us one <script src> and we
// cannot make it trust a second one. A classic IIFE with no import/export and
// no dynamic import() is also what lets a page with a strict CSP allow this
// embed by allowing one URL. scripts/assert-single-bundle.mjs fails the build
// if that ever stops being true.
export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  base: "./",
  build: {
    outDir: "dist/public",
    emptyOutDir: true,
    cssCodeSplit: false,
    lib: {
      entry: "src/public.ts",
      formats: ["iife"],
      name: "CassiniMeetingEmbed",
      fileName: () => "viewer.js",
    },
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
        manualChunks: undefined,
        assetFileNames: (assetInfo) =>
          assetInfo.name?.endsWith(".css") ? "viewer.css" : "[name][extname]",
      },
    },
  },
});

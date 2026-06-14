import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

// Embedded-viewer build.
//
// Emits ONE classic IIFE (dist/embedded/embedded.js) + ONE stylesheet
// (dist/embedded/embedded.css), the assets the operator serves at
// /ui/viewer.js and /ui/viewer.css and AppAPI registers as the viewer
// top-menu ui/script + ui/style. The page CSP is
// `script-src-elem 'strict-dynamic' 'nonce-…'`, so the bundle MUST be a
// self-contained classic script with no ESM import/export and no dynamic
// import() (strict-dynamic would not trust anything those load). The pins
// below — lib IIFE format, inlineDynamicImports, cssCodeSplit:false,
// manualChunks:undefined — guarantee the single-file shape; a post-build
// grep assertion in package.json fails the build if `import(` or a top-level
// export/import statement ever slips through.
//
// base is "./" so nothing is hardcoded at build time: the runtime proxy base
// comes from window.__CASSINI_VIEWER_BASE__, captured by src/embedded.ts.
export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  base: "./",
  build: {
    outDir: "dist/embedded",
    emptyOutDir: true,
    cssCodeSplit: false,
    lib: {
      entry: "src/embedded.ts",
      formats: ["iife"],
      name: "CassiniViewer",
      fileName: () => "embedded.js",
    },
    rollupOptions: {
      output: {
        inlineDynamicImports: true,
        manualChunks: undefined,
        // Single CSS asset -> embedded.css (Vite names the lib CSS after the
        // entry by default, but pin it so the operator/Dockerfile path is
        // stable regardless of Vite internals).
        assetFileNames: (assetInfo) => {
          if (assetInfo.name && assetInfo.name.endsWith(".css")) {
            return "embedded.css";
          }
          return "[name][extname]";
        },
      },
    },
  },
});

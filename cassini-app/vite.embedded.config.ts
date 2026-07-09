import { defineConfig } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

// Embedded control-panel build (D-382), mirroring vite.embedded.config.ts in
// cassini-viewer (D-381).
//
// Emits ONE classic IIFE (dist/embedded/embedded.js) + ONE stylesheet
// (dist/embedded/embedded.css), the assets the operator serves at
// /ui/control-panel.js and /ui/control-panel.css and AppAPI registers as the
// control-panel top-menu ui/script + ui/style. The page CSP is
// `script-src-elem 'strict-dynamic' 'nonce-…'`, so the bundle MUST be a
// self-contained classic script with no ESM import/export and no dynamic
// import() (strict-dynamic would not trust anything those load). The pins
// below — lib IIFE format, inlineDynamicImports, cssCodeSplit:false,
// manualChunks:undefined — guarantee the single-file shape; the post-build
// assert-embedded-single-bundle.mjs in package.json fails the build if
// `import(` or a top-level export/import statement ever slips through.
//
// base is "./" so nothing is hardcoded at build time: the runtime operator
// base path comes from window.__CASSINI_CONFIG__.operatorBasePath, set by
// src/embedded.ts from the captured proxy base. __CASSINI_OPERATOR_BASE_PATH__
// is still defined here because operator/config.ts references it as a
// build-time constant in its (unreached on the embedded page) fallback branch;
// leaving it undefined would make esbuild emit a dangling identifier.
export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  base: "./",
  // The standalone build copies public/cassini-config.js (the index.html shim
  // that ensures window.__CASSINI_CONFIG__ exists) into dist/. The embedded
  // bundle sets that object itself in src/embedded.ts and ships no index.html,
  // so disable publicDir — otherwise cassini-config.js lands in dist/embedded/
  // as a SECOND .js, tripping assert-embedded-single-bundle.mjs.
  publicDir: false,
  define: {
    __CASSINI_OPERATOR_BASE_PATH__: JSON.stringify("/operator"),
  },
  build: {
    outDir: "dist/embedded",
    emptyOutDir: true,
    cssCodeSplit: false,
    lib: {
      entry: "src/embedded.ts",
      formats: ["iife"],
      name: "CassiniApp",
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

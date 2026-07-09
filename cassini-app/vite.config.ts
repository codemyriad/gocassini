import { defineConfig, loadEnv, type ProxyOptions } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const operatorBasePath = normalizeOperatorBasePath(
    env.CASSINI_OPERATOR_BASE_PATH ?? process.env.CASSINI_OPERATOR_BASE_PATH ?? "/",
  );
  const operatorProxyTarget = (env.CASSINI_OPERATOR_URL ?? process.env.CASSINI_OPERATOR_URL ?? "").trim();
  const proxy = createOperatorProxy(operatorBasePath, operatorProxyTarget);

  return {
    plugins: [tailwindcss(), svelte()],
    base: "./",
    define: {
      __CASSINI_OPERATOR_BASE_PATH__: JSON.stringify(operatorBasePath),
    },
    // Pin a single JS + single CSS bundle in lock-step with
    // vite.embedded.config.ts. The control panel has no code-split /
    // dynamic-import / worker / wasm today, so this is effectively a no-op now;
    // pinning it keeps the standalone and embedded builds from silently
    // diverging into multi-chunk output if a future dependency introduces a
    // dynamic import.
    build: {
      cssCodeSplit: false,
      rollupOptions: {
        output: {
          inlineDynamicImports: true,
          manualChunks: undefined,
        },
      },
    },
    server: {
      cors: true,
      proxy,
    },
    preview: {
      cors: true,
      proxy,
    },
  };
});

function createOperatorProxy(operatorBasePath: string, operatorProxyTarget: string): Record<string, string | ProxyOptions> {
  if (operatorProxyTarget === "") {
    return {};
  }

  if (operatorBasePath === "/") {
    return {
      "/jobs": directOperatorProxy(operatorProxyTarget),
      "/events": directOperatorProxy(operatorProxyTarget),
    };
  }

  return {
    [operatorBasePath]: directOperatorProxy(operatorProxyTarget),
  };
}

function directOperatorProxy(operatorProxyTarget: string): ProxyOptions {
  return {
    target: operatorProxyTarget,
    changeOrigin: true,
    secure: false,
    timeout: 0,
    proxyTimeout: 0,
  };
}

function normalizeOperatorBasePath(value: string): string {
  const trimmed = value.trim();
  const normalized = (trimmed === "" ? "/" : trimmed).replace(/\/+$/, "") || "/";
  if (!normalized.startsWith("/")) {
    throw new Error("CASSINI_OPERATOR_BASE_PATH must start with '/'.");
  }
  return normalized;
}


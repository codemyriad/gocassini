import { defineConfig, loadEnv, type ProxyOptions } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  const operatorBasePath = normalizeOperatorBasePath(
    env.CASSINI_OPERATOR_BASE_PATH ?? process.env.CASSINI_OPERATOR_BASE_PATH ?? "/operator",
  );
  const operatorProxyTarget = (env.CASSINI_OPERATOR_URL ?? process.env.CASSINI_OPERATOR_URL ?? "").trim();
  const proxy = createOperatorProxy(operatorBasePath, operatorProxyTarget);

  return {
    plugins: [tailwindcss(), svelte()],
    base: "./",
    define: {
      __CASSINI_OPERATOR_BASE_PATH__: JSON.stringify(operatorBasePath),
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

  const prefixPattern = new RegExp(`^${escapeRegExp(operatorBasePath)}`);
  return {
    [operatorBasePath]: {
      target: operatorProxyTarget,
      changeOrigin: true,
      secure: false,
      timeout: 0,
      proxyTimeout: 0,
      rewrite(path) {
        const rewritten = path.replace(prefixPattern, "");
        return rewritten === "" ? "/" : rewritten;
      },
    },
  };
}

function normalizeOperatorBasePath(value: string): string {
  const trimmed = value.trim();
  const normalized = (trimmed === "" ? "/operator" : trimmed).replace(/\/+$/, "") || "/";
  if (!normalized.startsWith("/")) {
    throw new Error("CASSINI_OPERATOR_BASE_PATH must start with '/'.");
  }
  return normalized;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

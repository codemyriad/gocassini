import { defineConfig, loadEnv } from "vite";
import { svelte } from "@sveltejs/vite-plugin-svelte";
import tailwindcss from "@tailwindcss/vite";

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "");
  return {
    plugins: [tailwindcss(), svelte()],
    base: "./",
    define: {
      __CASSINI_OPERATOR_URL__: JSON.stringify(env.CASSINI_OPERATOR_URL ?? process.env.CASSINI_OPERATOR_URL ?? ""),
    },
  };
});

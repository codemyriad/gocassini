import { playwright } from "@vitest/browser-playwright";
import { configDefaults, defineConfig, mergeConfig } from "vitest/config";
import viteConfig from "./vite.config";

export default mergeConfig(viteConfig, defineConfig({
  test: {
    attachmentsDir: "./node_modules/.cache/vitest-attachments",
    projects: [
      {
        extends: true,
        test: {
          name: "unit",
          environment: "node",
          exclude: [...configDefaults.exclude, "**/*.browser.test.ts"],
        },
      },
      {
        extends: true,
        test: {
          name: "browser",
          include: ["src/**/*.browser.test.ts"],
          browser: {
            enabled: true,
            headless: true,
            provider: playwright({ actionTimeout: 5000 }),
            instances: [{ browser: "chromium" }],
            viewport: { width: 1440, height: 1000 },
            screenshotDirectory: "./node_modules/.cache/vitest-screenshots",
          },
        },
      },
    ],
  },
}));

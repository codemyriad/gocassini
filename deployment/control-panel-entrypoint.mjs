#!/usr/bin/env node
import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

const appDir = "/app";
const operatorBasePath = normalizeOperatorBasePath(process.env.CASSINI_OPERATOR_BASE_PATH ?? "/");

writeFileSync(
  `${appDir}/dist/cassini-config.js`,
  `window.__CASSINI_CONFIG__ = ${JSON.stringify({ operatorBasePath })};\n`,
  "utf8",
);

const child = spawn("npm", ["run", "preview", "--", "--host", "0.0.0.0", "--port", "4173"], {
  cwd: appDir,
  env: process.env,
  stdio: "inherit",
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    child.kill(signal);
  });
}

child.on("exit", (code) => {
  process.exit(code ?? 1);
});

function normalizeOperatorBasePath(value) {
  const trimmed = value.trim();
  const normalized = (trimmed === "" ? "/" : trimmed).replace(/\/+$/, "") || "/";
  if (!normalized.startsWith("/")) {
    throw new Error("CASSINI_OPERATOR_BASE_PATH must start with '/'.");
  }
  return normalized;
}

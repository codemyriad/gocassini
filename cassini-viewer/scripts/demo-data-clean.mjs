import { existsSync, rmSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { defaultDemoDataDir, parseArgs as parsePullArgs } from "./demo-data-pull.mjs";

const scriptDir = dirname(fileURLToPath(import.meta.url));

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  main().catch((error) => {
    console.error(error instanceof Error ? error.message : String(error));
    process.exitCode = 1;
  });
}

export async function main(argv = process.argv.slice(2), options = {}) {
  const { log = console.log } = options;
  const { outputDir } = parseArgs(argv);
  rmSync(outputDir, { recursive: true, force: true });
  log(existsSync(outputDir) ? `demo data still present -> ${outputDir}` : `demo data cleared -> ${outputDir}`);
}

export function parseArgs(argv) {
  return parsePullArgs(argv);
}

export { defaultDemoDataDir };

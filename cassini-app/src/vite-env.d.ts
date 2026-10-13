/// <reference types="vite/client" />

declare const __CASSINI_OPERATOR_BASE_PATH__: string;
// Set by vite.embedded.config.ts only; absent in tests and the standalone build.
declare const __CASSINI_BUILD_ID__: string | undefined;

declare global {
  interface Window {
    __CASSINI_CONFIG__?: {
      operatorBasePath?: string;
    };
  }
}

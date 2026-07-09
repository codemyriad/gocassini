/// <reference types="vite/client" />

declare const __CASSINI_OPERATOR_BASE_PATH__: string;

declare global {
  interface Window {
    __CASSINI_CONFIG__?: {
      operatorBasePath?: string;
    };
  }
}

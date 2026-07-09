// Ambient globals the shell reads on the AppAPI embedded page (D-420).
// __CASSINI_VIEWER_BASE__ is the captured proxy base the viewing layer resolves
// its catalog / artifact fetches against (set by src/embedded.ts).
declare global {
  interface Window {
    __CASSINI_VIEWER_BASE__?: string;
  }
}

export {};

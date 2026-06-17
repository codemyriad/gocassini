// Ambient declarations shared across the viewer.

declare global {
  interface Window {
    // Set synchronously by src/embedded.ts at top-level eval when the viewer
    // runs as the AppAPI-registered ui/script on Nextcloud's embedded page.
    // Holds the AppAPI proxy base for this ExApp, e.g.
    // "/index.php/apps/app_api/proxy/gocassini/". When present, catalog and
    // asset URLs resolve against it instead of import.meta.env.BASE_URL.
    // Absent in the standalone export build.
    __CASSINI_VIEWER_BASE__?: string;
  }
}

export {};

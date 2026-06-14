/*
 * Cassini top-menu iframe bootstrap, loaded by AppAPI's embedded ExApp page.
 *
 * USED ONLY BY THE control-panel ENTRY (D-381). The viewer entry no longer
 * uses this bootstrap: its embedded build (a self-mounting IIFE) is served at
 * /ui/viewer.js and mounts the SPA directly on the embedded page, because the
 * page CSP is script-src-elem 'strict-dynamic' 'nonce-…' and the viewer must
 * run in that nonce context rather than inside a frame. The admin control
 * panel works fine inside an iframe, so it stays on this bootstrap.
 *
 * AppAPI renders /apps/app_api/embedded/<app-id>/<entry> as a bare
 * <div id="content"></div> plus the scripts the ExApp registered for that
 * top-menu entry (POST /ocs/v2.php/apps/app_api/api/v1/ui/script). The
 * ExAppUiMiddleware rewrites our registered path to
 * /index.php/apps/app_api/proxy/<app-id>/ui/<entry>.js, whose src tells us both
 * the proxy base URL and which entry is being shown. The SPA for an entry lives
 * at <proxy base>/<entry>/ (entry names double as URL prefixes: control-panel/);
 * we fill #content with an iframe pointing there. The embedded page's CSP allows
 * same-host frames, and the iframe shares the user's Nextcloud session, so the
 * proxy's per-route access levels keep applying inside the frame.
 *
 * AppAPI loads this script with `defer`, so document.currentScript is null at
 * execution — scan document.scripts for our own /ui/<entry>.js src instead.
 */
(function () {
	"use strict";

	var match = null;
	var scripts = document.scripts || document.getElementsByTagName("script");
	for (var i = scripts.length - 1; i >= 0; i--) {
		var src = (scripts[i] && scripts[i].src ? scripts[i].src : "").split(/[?#]/)[0];
		var m = src.match(/^(.*)\/ui\/([^\/]+)\.js$/);
		if (m) {
			match = m;
			break;
		}
	}
	if (!match) {
		return;
	}
	var target = match[1] + "/" + match[2] + "/";

	function mount() {
		var content = document.getElementById("content");
		if (!content) {
			return;
		}
		content.style.height = "calc(100vh - var(--header-height, 50px))";
		var frame = document.createElement("iframe");
		frame.src = target;
		frame.title = "Cassini";
		frame.allow = "fullscreen";
		frame.style.display = "block";
		frame.style.width = "100%";
		frame.style.height = "100%";
		frame.style.border = "0";
		content.appendChild(frame);
	}

	if (document.readyState === "loading") {
		document.addEventListener("DOMContentLoaded", mount);
	} else {
		mount();
	}
})();

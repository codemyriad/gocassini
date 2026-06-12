/*
 * Cassini top-menu bootstrap, loaded by AppAPI's embedded ExApp page.
 *
 * AppAPI renders /apps/app_api/embedded/<app-id>/<entry> as a bare
 * <div id="content"></div> plus the scripts the ExApp registered for that
 * top-menu entry (POST /ocs/v2.php/apps/app_api/api/v1/ui/script). The
 * ExAppUiMiddleware rewrites our registered path to
 * /index.php/apps/app_api/proxy/<app-id>/ui/<entry>.js, so
 * document.currentScript.src tells us both the proxy base URL and which
 * entry is being shown. The SPA for an entry lives at <proxy base>/<entry>/
 * (entry names double as URL prefixes: viewer/, control-panel/); we fill
 * #content with an iframe pointing there. The embedded page's CSP allows
 * same-host frames, and the iframe shares the user's Nextcloud session, so
 * the proxy's per-route access levels keep applying inside the frame.
 */
(function () {
	"use strict";

	var script = document.currentScript;
	if (!script || !script.src) {
		return;
	}
	var src = script.src.split(/[?#]/)[0];
	var match = src.match(/^(.*)\/ui\/([^\/]+)\.js$/);
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

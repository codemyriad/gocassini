#!/usr/bin/env bash
# Build the cassini_capture native-app package that delivers the source-audio
# payload to Talk through LoadAdditionalScriptsEvent.

set -euo pipefail

usage() {
	cat >&2 <<'EOF'
Usage: ./scripts/build-capture-companion.sh [--version VERSION]
       [--staging DIR] [--output FILE] [--skip-js-build]
EOF
}

die() { echo "error: $*" >&2; exit 1; }

version=""
staging=""
output=""
skip_js=0
while [[ $# -gt 0 ]]; do
	case "$1" in
		--version) version="${2:?--version needs a value}"; shift 2 ;;
		--staging) staging="${2:?--staging needs a directory}"; shift 2 ;;
		--output) output="${2:?--output needs a file}"; shift 2 ;;
		--skip-js-build) skip_js=1; shift ;;
		-h|--help) usage; exit 0 ;;
		*) die "unknown argument: $1" ;;
	esac
done

root="$(git rev-parse --show-toplevel)"
manifest="$root/cassini_capture/appinfo/info.xml"
[[ -f "$manifest" ]] || die "missing companion manifest: $manifest"
manifest_version="$(sed -n 's|.*<version>\(.*\)</version>.*|\1|p' "$manifest" | head -n1)"
exapp_version="$(sed -n 's|.*<version>\(.*\)</version>.*|\1|p' "$root/appinfo/info.xml" | head -n1)"
version="${version:-$manifest_version}"
[[ "$manifest_version" == "$version" ]] \
	|| die "cassini_capture manifest is $manifest_version, not requested $version"
[[ "$exapp_version" == "$version" ]] \
	|| die "companion $version and gocassini $exapp_version must be released together"

if [[ "$skip_js" -eq 0 ]]; then
	( cd "$root" && npm run build:capture -w cassini-app )
fi
payload="$root/cassini-app/dist/capture/capture-payload.js"
[[ -s "$payload" ]] || die "missing built payload: $payload"

staging="${staging:-$root/build/capture-companion}"
output="${output:-$root/build/artifacts/capture-companion/cassini_capture.tar.gz}"
tree="$staging/cassini_capture"
rm -rf "$tree"
mkdir -p "$tree/appinfo" "$tree/lib/AppInfo" "$tree/lib/Listener" "$tree/js" "$(dirname "$output")"

cp "$root/cassini_capture/appinfo/info.xml" "$root/cassini_capture/appinfo/app.php" "$tree/appinfo/"
cp "$root/cassini_capture/lib/AppInfo/Application.php" "$tree/lib/AppInfo/"
cp "$root/cassini_capture/lib/Listener/LoadTalkCaptureScriptListener.php" "$tree/lib/Listener/"
cp "$root/cassini_capture/README.md" "$root/LICENSE" "$tree/"
cp "$payload" "$tree/js/capture-payload.js"

tar -czf "$output" --owner=0 --group=0 -C "$staging" cassini_capture
echo "Wrote ${output#"$root"/} ($(wc -c <"$output") bytes)"

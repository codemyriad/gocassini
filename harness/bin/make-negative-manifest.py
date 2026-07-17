#!/usr/bin/env python3
"""Emit a manifest identical to the source except that the signaling-secret
declaration is removed.

This models the exact D-403 regression the faithful installed-ExApp gate exists
to guard: an appinfo/info.xml that no longer declares
CASSINI_TALK_SIGNALING_INTERNAL_SECRET under <environment-variables>. AppAPI's
ExAppService merges admin-supplied deploy options (`env KEY=VALUE` on
`occ app_api:app:register`) into the manifest's *declared* set and silently
drops undeclared keys, so an install driven by this manifest registers fine but
reports the signaling secret unconfigured and fails route verification before
any recording is attempted.

The fixture is GENERATED, never checked in: <version>, <image-tag>, every route,
and every sibling <variable> are copied untouched, so the negative manifest
cannot drift out of sync with appinfo/info.xml as versions bump
(scripts/release-version.sh only edits appinfo/info.xml). Clones the ElementTree
remove-node idiom in harness/bin/test-exapp-manifest.sh.

Usage: make-negative-manifest.py <source-info.xml> <target-info.xml>
Exits non-zero unless exactly one matching <variable> was found and removed.
"""
import sys
from xml.etree import ElementTree as ET

TARGET_VARIABLE = "CASSINI_TALK_SIGNALING_INTERNAL_SECRET"


def main(argv):
    if len(argv) != 3:
        raise SystemExit(f"usage: {argv[0]} <source-info.xml> <target-info.xml>")
    source, target = argv[1], argv[2]

    try:
        tree = ET.parse(source)
    except (OSError, ET.ParseError) as exc:
        raise SystemExit(f"error: could not parse {source}: {exc}")

    env_vars = tree.getroot().find("./external-app/environment-variables")
    if env_vars is None:
        raise SystemExit(
            f"error: {source} has no <external-app>/<environment-variables>"
        )

    removed = 0
    for variable in list(env_vars.findall("variable")):
        if (variable.findtext("name") or "").strip() == TARGET_VARIABLE:
            env_vars.remove(variable)
            removed += 1

    if removed != 1:
        raise SystemExit(
            f"error: expected exactly one <variable> named {TARGET_VARIABLE} in "
            f"{source}, removed {removed}"
        )

    # Preserve the XML declaration so the emitted manifest is byte-shaped like
    # the one AppAPI's PHP parser reads on `app_api:app:register`.
    tree.write(target, encoding="unicode", xml_declaration=True)


if __name__ == "__main__":
    main(sys.argv)

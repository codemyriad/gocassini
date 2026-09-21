#!/usr/bin/env bash
# Registry maintenance only; never part of image/release qualification.
# Default to a read-only plan. A scheduled/manual workflow explicitly opts into
# deletion, preserving named tags, retained index children and the upload grace.
set -euo pipefail
: "${OWNER:?repository owner required}"
: "${GH_TOKEN:?GitHub package access required}"
: "${RUNNER_TEMP:?isolated maintenance directory required}"
DRY_RUN="${DRY_RUN:-true}"
case "$DRY_RUN" in true|false) ;; *) echo 'DRY_RUN must be true or false' >&2; exit 1 ;; esac

delete_version() {
  if [[ "$DRY_RUN" == true ]]; then
    echo "DRY RUN: would delete ${PACKAGE} version $1"
  else
    gh api -X DELETE -H "Accept: application/vnd.github+json" \
      "/orgs/${OWNER}/packages/container/${PACKAGE}/versions/$1"
  fi
}

case "${1:-}" in
  images)
    PACKAGE=gocassini
    KEEP="${KEEP:-10}"
    UNTAGGED_GRACE_SECONDS="${UNTAGGED_GRACE_SECONDS:-7200}"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    # Fetch every version of the container package.
    gh api --paginate \
      -H "Accept: application/vnd.github+json" \
      "/orgs/${OWNER}/packages/container/${PACKAGE}/versions" \
      > "$tmp/versions.json"

    # Protect manifests referenced BY a kept tag. A multi-arch index is
    # itself tagged, but the per-platform manifests it points at are
    # untagged — so a tag-only keep rule happily deletes the actual
    # image out from under a tag it just decided to keep forever.
    # We must protect child manifests of both keep-forever tags AND
    # the rolling $KEEP retained SHA tags so multi-arch images remain pullable.
    echo "${GH_TOKEN}" | docker login ghcr.io -u "${OWNER}" --password-stdin >/dev/null
    : > "$tmp/protected.txt"

    # 1. Keep-forever tag set. Persist it under RUNNER_TEMP so the
    # verify step below asserts pullability over exactly the tags this step
    # decided to keep, instead of a second hardcoded list that can drift
    # from this predicate (D-567).
    jq -r -s '
      add // []
      | .[] | .metadata.container.tags[]?
      | select(. == "latest" or . == "latest-cuda" or . == "latest-rocm"
               or . == "cuda" or startswith("v")
               or test("^[0-9]+\\.[0-9]+\\.[0-9]+"))
    ' "$tmp/versions.json" | sort -u > "${RUNNER_TEMP}/keep-tags.txt"

    # 2. Candidate tagged versions for rolling retention: tagged with sha-*, branch-*, etc.
    # Sort newest -> oldest by creation time across all pages.
    jq -r -s '
      add // []
      | sort_by(.created_at) | reverse
      | .[]
      | select(.metadata.container.tags | length > 0)
      | select(
          (.metadata.container.tags | any(. == "latest" or . == "latest-cuda" or . == "latest-rocm" or . == "cuda" or startswith("v") or test("^[0-9]+\\.[0-9]+\\.[0-9]+"))) | not
        )
      | "\(.id)\t\(.name)\t\(.created_at)\t\(.metadata.container.tags | join(","))"
    ' "$tmp/versions.json" > "$tmp/candidates-tagged.tsv"

    # Retain the newest $KEEP tagged versions, and collect tags to protect their child manifests.
    head -n "${KEEP}" "$tmp/candidates-tagged.tsv" | awk -F'\t' '{print $4}' | tr ',' '\n' | grep -v '^$' > "$tmp/retained-rolling-tags.txt" || true

    # Combine keep-forever tags and retained rolling tags for manifest protection.
    cat "${RUNNER_TEMP}/keep-tags.txt" "$tmp/retained-rolling-tags.txt" | sort -u > "$tmp/all-retained-tags.txt"

    while read -r tag; do
      [[ -z "$tag" ]] && continue
      if ! raw="$(docker buildx imagetools inspect --raw "ghcr.io/${OWNER}/${PACKAGE}:${tag}")"; then
        echo "::error::Failed to inspect retained tag ghcr.io/${OWNER}/${PACKAGE}:${tag}; aborting prune to protect manifests" >&2
        exit 1
      fi
      echo "$raw" | jq -r '.manifests[]?.digest // empty' >> "$tmp/protected.txt"
    done < "$tmp/all-retained-tags.txt"
    sort -u -o "$tmp/protected.txt" "$tmp/protected.txt"
    echo "Protected $(wc -l < "$tmp/protected.txt") child manifest(s) referenced by kept tags."

    # Versions worth keeping outright (any named keep-forever tag).
    jq -r -s '
      add // []
      | .[]
      | .metadata.container.tags as $tags
      | select(
          ($tags | any(. == "latest" or . == "latest-cuda" or . == "latest-rocm" or . == "cuda" or startswith("v") or test("^[0-9]+\\.[0-9]+\\.[0-9]+")))
        )
      | "KEEP \(.id) tags=\($tags | join(","))"
    ' "$tmp/versions.json" | tee "$tmp/keep.log" || true

    # 3. Prune tagged versions beyond the newest $KEEP.
    tail -n +$((KEEP + 1)) "$tmp/candidates-tagged.tsv" | while IFS=$'\t' read -r id digest created tags; do
      [[ -z "$id" ]] && continue
      if grep -qxF "${digest}" "$tmp/protected.txt"; then
        echo "SKIP   tagged id=${id} digest=${digest} — referenced by a retained tag"
        continue
      fi
      echo "DELETE tagged id=${id} digest=${digest} created=${created} tags=${tags}"
      delete_version "$id"
    done

    # 4. Prune untagged child manifests that are not referenced by ANY retained tag,
    # and are older than UNTAGGED_GRACE_SECONDS to protect in-progress uploads.
    now=$(date +%s)
    cutoff=$((now - UNTAGGED_GRACE_SECONDS))

    jq -r -s '
      add // []
      | .[]
      | select(.metadata.container.tags | length == 0)
      | "\(.id)\t\(.name)\t\(.created_at)"
    ' "$tmp/versions.json" > "$tmp/untagged-manifests.tsv"

    while IFS=$'\t' read -r id digest created; do
      [[ -z "$id" ]] && continue
      if grep -qxF "${digest}" "$tmp/protected.txt"; then
        echo "SKIP   untagged id=${id} digest=${digest} — referenced by a retained tag"
        continue
      fi
      created_sec=$(date -u -d "${created}" +%s 2>/dev/null || echo 0)
      if [ "${created_sec}" -gt "${cutoff}" ]; then
        echo "SKIP   untagged id=${id} digest=${digest} created=${created} — within upload grace period"
        continue
      fi
      echo "DELETE untagged id=${id} digest=${digest} created=${created} (unreferenced manifest)"
      delete_version "$id"
    done < "$tmp/untagged-manifests.tsv"
    ;;
  cuda-base)
    PACKAGE=gocassini-cuda-base
    KEEP="${KEEP:-5}"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' EXIT

    # The package may not exist yet on the very first base build cycle.
    if ! gh api --paginate \
        -H "Accept: application/vnd.github+json" \
        "/orgs/${OWNER}/packages/container/${PACKAGE}/versions" \
        > "$tmp/versions.json" 2>/dev/null; then
      echo "Package ${PACKAGE} not found yet — nothing to prune."
      exit 0
    fi

    jq -r -s '(add // []) | sort_by(.created_at) | reverse | .[] | "\(.id)\t\(.created_at)\t\(.metadata.container.tags | join(","))"' \
      "$tmp/versions.json" > "$tmp/all.tsv"
    total=$(wc -l < "$tmp/all.tsv")
    echo "Found ${total} base versions; keeping newest ${KEEP}"
    if [[ "${total}" -le "${KEEP}" ]]; then
      echo "Nothing to prune."
      exit 0
    fi
    tail -n +$((KEEP + 1)) "$tmp/all.tsv" | while IFS=$'\t' read -r id created tags; do
      echo "DELETE id=${id} created=${created} tags=${tags}"
      delete_version "$id"
    done
    ;;
  verify)
    PACKAGE=gocassini
    echo "${GH_TOKEN}" | docker login ghcr.io -u "${OWNER}" --password-stdin >/dev/null

    # Written by the Prune step from the same predicate it keeps by. A
    # missing or empty list means the keep rule matched nothing, which
    # would silently verify zero tags — the exact failure mode this gate
    # exists to prevent. Fail instead.
    keep_tags="${RUNNER_TEMP}/keep-tags.txt"
    if [ ! -s "${keep_tags}" ]; then
      echo "FAIL: no keep-forever tags resolved from the prune step; refusing to verify nothing"
      exit 1
    fi
    echo "Verifying $(wc -l < "${keep_tags}") keep-forever tag(s)."

    failed=0
    while read -r tag; do
      ref="ghcr.io/${OWNER}/${PACKAGE}:${tag}"
      if ! raw=$(docker buildx imagetools inspect --raw "${ref}" 2>/dev/null); then
        echo "FAIL ${tag}: tag does not resolve"; failed=1; continue
      fi
      # For an index, every referenced child must itself resolve.
      tag_ok=1
      for child in $(printf '%s' "${raw}" | jq -r '.manifests[]?.digest'); do
        if ! docker buildx imagetools inspect --raw \
             "ghcr.io/${OWNER}/${PACKAGE}@${child}" >/dev/null 2>&1; then
          echo "FAIL ${tag}: child ${child} is missing from the registry"
          tag_ok=0
          failed=1
        fi
      done
      if [ "${tag_ok}" -eq 1 ]; then echo "OK   ${tag}"; fi
    done < "${keep_tags}"
    if [ "${failed}" -ne 0 ]; then
      echo "::error::A keep-forever tag is not pullable. Either the manifest was"
      echo "::error::pruned out from under it, or the tag was never fully published."
      echo "::error::A dangling tag cannot be repaired by retagging — it needs a rebuild"
      echo "::error::at that source ref, or deletion if the version is not worth restoring."
    fi
    exit "${failed}"
    ;;
  *) echo 'Usage: prune-container-images.sh images|cuda-base|verify' >&2; exit 2 ;;
esac

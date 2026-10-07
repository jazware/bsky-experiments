#!/usr/bin/env bash
# Build a service's amd64 image with ko and push it to GHCR, without a Docker
# build (scripts/oci/README.md). The base is the runtime-base stage of
# build/<service>/Dockerfile, pinned in ../.ko.yaml; feedgen's dashboard is
# built first, since the binary embeds it.
#
#   build/ko-image.sh <indexer|feedgen|search|all> [tag]   (or: just image-push [service] [tag])
#   build/ko-image.sh <service> base                       (or: just image-base <service>)
#
# `all` exports the tree once and builds the three services at once, each its
# own build with its own image and tag (oci_all in scripts/oci/lib.sh).
#
# Builds the committed tree; uncommitted changes under packages/atproto,
# version or telemetry stop it (DIRTY=1 builds them as <sha>-dirty). Also tags
# :main when HEAD is on main and the tag is the default, since that's the tag
# the yeet stacks pin. PUSH=0 loads ko.local/atproto-<service> into docker.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
. "$here/../../../scripts/oci/lib.sh"
svc=${1:?usage: ko-image.sh <indexer|feedgen|search|all> [tag|base]}
case $svc in indexer | feedgen | search | all) ;; *) echo "ko-image: unknown service $svc" >&2; exit 2 ;; esac
src=(packages/atproto packages/version packages/telemetry)

if [ "$svc" = all ]; then
  [ "${2:-}" != base ] || { echo "ko-image: base takes one service" >&2; exit 2; }
  oci_init atproto ""
  oci_export "${src[@]}"
  oci_all "$0" "${2:-}" indexer feedgen search
  exit
fi

image=ghcr.io/jazware/mono/atproto-$svc
if [ "${2:-}" = base ]; then
  oci_init atproto "$image"
  oci_base_push "$here/$svc/Dockerfile" runtime-base "$here/../.ko.yaml" \
    "github.com/jazware/mono/packages/atproto/cmd/$svc"
  exit
fi

oci_tracked "$image" "${2:-}" "$0" "$@"
oci_init atproto "$image" "${2:-}"
oci_export "${src[@]}"
if [ "$svc" = feedgen ]; then
  oci_npm packages/atproto/dashboard
  oci_ui_into packages/atproto/dashboard/dist
fi
oci_ko packages/atproto "./cmd/$svc" "$image"
oci_tag_main main
_oci_summary

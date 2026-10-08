#!/usr/bin/env bash
# Build a service's production image on this Mac and push it to GHCR. If amd64
# containers fail with "exec format error", OrbStack lost its Rosetta
# registration: run `orb stop && orb start`.
#
#   build/mac-image.sh <indexer|feedgen|search> [tag]   (or: just docker-push <service> [tag])
#
# Builds the committed tree (vcs_archive), so uncommitted edits never
# reach an image. The Dockerfiles' context is packages/ and they only read
# atproto, version and telemetry, so only those are archived. Also pushes :main
# when HEAD is on main, since that's the tag the yeet stacks pin.
#
# Env: IMAGE (ghcr.io/jazware/mono/atproto-<service>), PLATFORM (linux/amd64), PUSH=0 (load only).
set -euo pipefail
svc=${1:?usage: mac-image.sh <indexer|feedgen|search> [tag]}
case $svc in indexer | feedgen | search) ;; *) echo "mac-image: unknown service $svc" >&2; exit 2 ;; esac
IMAGE=${IMAGE:-ghcr.io/jazware/mono/atproto-$svc}
. "$(dirname "$0")/../../../scripts/vcs.sh"
cd "$(vcs_root -C "$(dirname "$0")")"
commit=$(vcs_short)
git_sha=$(vcs_delta_git_sha)
tag=${2:-$commit}
tags=("$tag")
if [ "$tag" != main ] && vcs_on_main 2>/dev/null; then tags+=(main); fi
t_start=$(date +%s)
ctx=$(mktemp -d)
cfg=$(mktemp -d)
trap 'rm -rf "$ctx" "$cfg"' EXIT
vcs_archive HEAD packages/atproto packages/version packages/telemetry | tar -x -C "$ctx"
[ -f "$ctx/packages/atproto/build/$svc/Dockerfile" ] || { echo "mac-image: empty build context" >&2; exit 1; }
docker buildx build --platform "${PLATFORM:-linux/amd64}" \
  --build-arg "GIT_COMMIT=$commit" \
  --build-arg "BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  ${git_sha:+--label "dev.jazco.delta.git-sha=$git_sha"} \
  "${tags[@]/#/--tag=$IMAGE:}" -f "$ctx/packages/atproto/build/$svc/Dockerfile" --load "$ctx/packages"
if [ "${PUSH:-1}" = 1 ]; then
  # DOCKER_HOST keeps the current context's daemon (contexts live in DOCKER_CONFIG)
  host=$(docker context inspect -f '{{.Endpoints.docker.Host}}')
  gh auth token | DOCKER_CONFIG=$cfg DOCKER_HOST=$host docker login ghcr.io -u "$(gh api user -q .login)" --password-stdin >/dev/null
  for t in "${tags[@]}"; do DOCKER_CONFIG=$cfg DOCKER_HOST=$host docker push -q "$IMAGE:$t"; done
fi
printf 'mac-image: %s:{%s} in %ds\n' "$IMAGE" "${tags[*]}" $(($(date +%s) - t_start)) >&2

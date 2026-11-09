#!/usr/bin/env bash
# Build the local SHELF Docker image. Nothing is pushed or published.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
image="${SHELF_DOCKER_IMAGE:-shelf:local}"
docker="${DOCKER:-docker}"

while [ $# -gt 0 ]; do
  case "$1" in
    --image) [ $# -ge 2 ] || { echo "error: --image needs a value" >&2; exit 2; }; image="$2"; shift 2 ;;
    -h|--help)
      echo "usage: scripts/docker/build.sh [--image shelf:local]"
      echo "Builds a local image through 'make docker'. It never logs in or pushes."
      exit 0 ;;
    *) echo "error: unknown argument $1" >&2; exit 2 ;;
  esac
done

case "$image" in */*) echo "error: registry image names are not allowed: $image" >&2; exit 2 ;; esac
command -v "$docker" >/dev/null || { echo "error: Docker command not found: $docker" >&2; exit 2; }
exec make -C "$repo" docker DOCKER="$docker" DOCKER_IMAGE="$image"

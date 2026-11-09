#!/usr/bin/env bash
# Stop only the named SHELF container. Containers and volumes are preserved.
set -euo pipefail

name="${SHELF_DOCKER_NAME:-shelf}"
docker="${DOCKER:-docker}"
wait_seconds=15

while [ $# -gt 0 ]; do
  case "$1" in
    --name) [ $# -ge 2 ] || { echo "error: --name needs a value" >&2; exit 2; }; name="$2"; shift 2 ;;
    --wait) [ $# -ge 2 ] || { echo "error: --wait needs a value" >&2; exit 2; }; wait_seconds="$2"; shift 2 ;;
    -h|--help)
      echo "usage: scripts/docker/stop.sh [--name shelf] [--wait 15]"
      echo "Stops the named container. It never removes containers or volumes."
      exit 0 ;;
    *) echo "error: unknown argument $1" >&2; exit 2 ;;
  esac
done

[[ "$wait_seconds" =~ ^[0-9]+$ ]] || { echo "error: --wait must be seconds" >&2; exit 2; }
command -v "$docker" >/dev/null || { echo "error: Docker command not found: $docker" >&2; exit 2; }
if ! "$docker" container inspect "$name" >/dev/null 2>&1; then
  echo ">> no $name container; nothing was stopped"
  exit 0
fi
if [ "$($docker inspect --format '{{.State.Running}}' "$name")" != true ]; then
  echo ">> $name is already stopped"
  exit 0
fi
"$docker" stop --time "$wait_seconds" "$name" >/dev/null
echo ">> $name stopped; its container and volumes were kept"

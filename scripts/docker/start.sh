#!/usr/bin/env bash
# Create or start one local SHELF container with persistent named volumes.
set -euo pipefail

name="${SHELF_DOCKER_NAME:-shelf}"
image="${SHELF_DOCKER_IMAGE:-shelf:local}"
docker="${DOCKER:-docker}"
config=""
media=""
host_port=8080
config_volume="shelf-config"
cache_volume="shelf-cache"
restart=0

usage() {
  cat <<'EOF'
usage: scripts/docker/start.sh --config FILE --media DIR [options]

Required:
  --config FILE          container config: roots below /data, data_dir below
                         /config, cache_dir below /cache
  --media DIR            host media directory, mounted read-only at /data

Options:
  --name NAME            container name [shelf]
  --image IMAGE          local image [shelf:local]
  --port PORT            host loopback port [8080]
  --config-volume NAME   persistent config/data volume [shelf-config]
  --cache-volume NAME    disposable cache volume [shelf-cache]
  --restart              copy the config and restart an already running container

The container is never published beyond 127.0.0.1. This script does not change
cloudflared and does not remove containers or volumes.
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --config) [ $# -ge 2 ] || { echo "error: --config needs a value" >&2; exit 2; }; config="$2"; shift 2 ;;
    --media) [ $# -ge 2 ] || { echo "error: --media needs a value" >&2; exit 2; }; media="$2"; shift 2 ;;
    --name) [ $# -ge 2 ] || { echo "error: --name needs a value" >&2; exit 2; }; name="$2"; shift 2 ;;
    --image) [ $# -ge 2 ] || { echo "error: --image needs a value" >&2; exit 2; }; image="$2"; shift 2 ;;
    --port) [ $# -ge 2 ] || { echo "error: --port needs a value" >&2; exit 2; }; host_port="$2"; shift 2 ;;
    --config-volume) [ $# -ge 2 ] || { echo "error: --config-volume needs a value" >&2; exit 2; }; config_volume="$2"; shift 2 ;;
    --cache-volume) [ $# -ge 2 ] || { echo "error: --cache-volume needs a value" >&2; exit 2; }; cache_volume="$2"; shift 2 ;;
    --restart) restart=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown argument $1" >&2; usage >&2; exit 2 ;;
  esac
done

[ -n "$config" ] || { echo "error: --config is required" >&2; exit 2; }
[ -n "$media" ] || { echo "error: --media is required" >&2; exit 2; }
[ -f "$config" ] || { echo "error: config file not found: $config" >&2; exit 2; }
[ -d "$media" ] || { echo "error: media directory not found: $media" >&2; exit 2; }
if ! [[ "$host_port" =~ ^[0-9]+$ ]] || ! ((host_port > 0 && host_port < 65536)); then
  echo "error: --port must be between 1 and 65535" >&2
  exit 2
fi
case "$image" in */*) echo "error: registry image names are not allowed: $image" >&2; exit 2 ;; esac
command -v "$docker" >/dev/null || { echo "error: Docker command not found: $docker" >&2; exit 2; }
command -v python3 >/dev/null || { echo "error: python3 is required to validate the config" >&2; exit 2; }

container_port="$(python3 -I - "$config" <<'PY'
import os, sys, yaml
p = sys.argv[1]
try:
    d = yaml.safe_load(open(p)) or {}
    roots = d.get("roots") or []
    paths = [str(r.get("path", "")) for r in roots]
    data = str((d.get("storage") or {}).get("data_dir", ""))
    cache = str((d.get("storage") or {}).get("cache_dir", ""))
    server = d.get("server") or {}
    listen = str(server.get("listen", "0.0.0.0"))
    port = int(server.get("port", 8080))
except Exception as e:
    print(f"error: cannot read {p}: {e}", file=sys.stderr)
    raise SystemExit(2)
bad = []
if listen not in ("0.0.0.0", "::"):
    bad.append('server.listen must be "0.0.0.0" or "::" inside the container')
if not paths or any(os.path.commonpath(["/data", os.path.abspath(x)]) != "/data" for x in paths):
    bad.append("every roots[].path must be /data or below")
if os.path.commonpath(["/config", os.path.abspath(data)]) != "/config":
    bad.append("storage.data_dir must be /config or below")
if os.path.commonpath(["/cache", os.path.abspath(cache)]) != "/cache":
    bad.append("storage.cache_dir must be /cache or below")
if not 0 < port < 65536:
    bad.append("server.port must be between 1 and 65535")
if bad:
    print("error: config is not container-ready:", file=sys.stderr)
    for line in bad:
        print("  - " + line, file=sys.stderr)
    raise SystemExit(2)
print(port)
PY
)"

config="$(realpath -- "$config")"
media="$(realpath -- "$media")"
"$docker" image inspect "$image" >/dev/null 2>&1 || {
  echo "error: image $image does not exist; run scripts/docker/build.sh" >&2; exit 2;
}

exists=0
"$docker" container inspect "$name" >/dev/null 2>&1 && exists=1
if [ "$exists" = 0 ]; then
  "$docker" create --name "$name" \
    -p "127.0.0.1:$host_port:$container_port" \
    -v "$config_volume:/config" \
    -v "$cache_volume:/cache" \
    -v "$media:/data:ro" \
    "$image" >/dev/null
fi

running="$($docker inspect --format '{{.State.Running}}' "$name" 2>/dev/null || echo false)"
if [ "$running" = true ] && [ "$restart" = 0 ]; then
  echo ">> $name is already running at http://127.0.0.1:$host_port"
  exit 0
fi

"$docker" cp "$config" "$name:/config/shelf.yaml"
if [ "$running" = true ]; then
  "$docker" restart "$name" >/dev/null
else
  "$docker" start "$name" >/dev/null
fi

"$docker" inspect --format '{{.State.Running}}' "$name" | grep -qx true || {
  echo "error: $name did not stay running; inspect it with: docker logs $name" >&2; exit 1;
}
echo ">> $name is running from $image at http://127.0.0.1:$host_port"
echo ">> media: $media -> /data (read-only); config volume: $config_volume; cache volume: $cache_volume"

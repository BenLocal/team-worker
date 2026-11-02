#!/usr/bin/env bash
# Upgrade a Docker Compose deployment of team-worker to this checkout's
# binaries: build them here, copy them over with ah, add them as one layer on
# the base image, point the compose file at the new tag and recreate the
# container. The layer also carries the image's entrypoint scripts and the
# Dockerfile's team-workerd settings, so changes to those arrive too; other
# Dockerfile changes need a full image build. Recreating ends every agent, so it refuses while the team has
# in-progress tasks unless --force is given.
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)

usage() {
    cat <<'EOF'
Usage: scripts/deploy-compose.sh --host AH_NAME --dir COMPOSE_DIR --base IMAGE_TAG --remote NAME --team TEAM [--force]

  --host    ah connection of the Docker host, e.g. prod
  --dir     directory of docker-compose.yml on that host
  --base    image the binaries go on, e.g. team-worker-local:demo-fa98bfc
  --remote  config.json remote of the deployment, for the task check
  --team    team whose in-progress tasks block the recreate
  --force   recreate even with in-progress tasks
EOF
}

host= dir= base= remote= team= force=false
while [[ $# -gt 0 ]]; do
    case $1 in
        --host) host=$2; shift 2 ;;
        --dir) dir=$2; shift 2 ;;
        --base) base=$2; shift 2 ;;
        --remote) remote=$2; shift 2 ;;
        --team) team=$2; shift 2 ;;
        --force) force=true; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
    esac
done
if [[ -z $host || -z $dir || -z $base || -z $remote || -z $team ]]; then
    usage >&2
    exit 2
fi
if [[ -n $(git -C "$ROOT" status --porcelain) ]]; then
    echo "the working tree has uncommitted changes; commit them so the image tag names what it holds" >&2
    exit 1
fi
rev=$(git -C "$ROOT" rev-parse --short HEAD)
# demo-fa98bfc becomes demo-<rev>.
image="${base%-*}-$rev"
build="$dir/build-$rev"

# The check runs on its own and the recreate waits for its answer.
open=$(team-worker --remote "$remote" --json task list "$team" | python3 -c '
import json, sys
print(" ".join(t["id"] for t in json.load(sys.stdin) if t["status"] == "in_progress"))')
if [[ -n $open && $force != true ]]; then
    echo "team $team has in-progress tasks ($open); recreating would end their agents. Wait, or pass --force." >&2
    exit 1
fi

# The daemon's settings come from the Dockerfile, so the layer cannot drift from it.
daemon_env=$(grep -oE 'TEAM_WORKER_(HTTP_ADDR|BOARD|WEB_BASE_PATH|BOARD_LINKS)=("[^"]*"|[^ \\]+)' "$ROOT/docker/Dockerfile" | tr '\n' ' ')
if [[ $(wc -w <<<"$daemon_env") -ne 4 ]]; then
    echo "could not read the four team-workerd settings from docker/Dockerfile" >&2
    exit 1
fi

echo "building binaries for $rev"
"$ROOT/docker/team-worker.sh" bins >/dev/null
cp "$ROOT/docker/container-entrypoint.sh" "$ROOT/docker/docker-entrypoint.sh" "$ROOT/bin/docker/"
ah --timeout 240s cp -r "$ROOT/bin/docker" "$host:$build"
local_sums=$(cd "$ROOT/bin/docker" && sha256sum team-worker team-workerd team-worker-ops)
remote_sums=$(ah --timeout 60s connect "$host" "cd '$build' && sha256sum team-worker team-workerd team-worker-ops")
if [[ $local_sums != "$remote_sums" ]]; then
    echo "the copied binaries do not match the built ones" >&2
    exit 1
fi

echo "building $image on $host and recreating the container"
ah --timeout 600s connect "$host" "set -e
cd '$build'
# The host may only have the legacy builder: no COPY --chmod.
cat > Dockerfile <<'DOCKERFILE'
FROM $base
USER root
COPY team-worker team-workerd team-worker-ops /usr/local/bin/
COPY container-entrypoint.sh /usr/local/bin/container-entrypoint
COPY docker-entrypoint.sh /usr/local/bin/team-worker-docker-entrypoint
RUN chmod 755 /usr/local/bin/container-entrypoint /usr/local/bin/team-worker-docker-entrypoint
ENV $daemon_env
USER node
DOCKERFILE
docker build -q -t '$image' . >/dev/null
cd '$dir'
cp -p docker-compose.yml bak.docker-compose.yml.bak-\$(date +%Y%m%dT%H%M%S)
sed -i -E 's|^(    image: ).*|\1$image|' docker-compose.yml
docker-compose config >/dev/null
docker-compose up -d"

echo "waiting for the container to settle"
sleep 60
ah --timeout 60s connect "$host" "docker inspect \$(cd '$dir' && docker-compose ps -q) --format 'image={{.Config.Image}} restarts={{.RestartCount}}'"
team-worker --remote "$remote" agent list "$team"

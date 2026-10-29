#!/usr/bin/env bash
set -euo pipefail

as_agent() {
    if [[ $(id -u) == 0 ]]; then
        setpriv --reuid=node --regid=node --init-groups "$@"
    else
        "$@"
    fi
}
as_agent team-worker-ops proxy

case ${TEAM_WORKER_DIND:-0} in
    0) ;;
    1) ;;
    *) echo 'TEAM_WORKER_DIND must be 0 or 1.' >&2; exit 1 ;;
esac
if [[ ${TEAM_WORKER_DIND:-0} == 0 ]]; then
    if [[ $(id -u) == 0 ]]; then
        exec setpriv --reuid=node --regid=node --init-groups container-entrypoint "$@"
    fi
    exec container-entrypoint "$@"
fi
if [[ $(id -u) != 0 ]]; then
    echo 'DinD needs --user root --privileged, or user: root and privileged: true in Compose.' >&2
    exit 1
fi
if [[ ! -w /sys/fs/cgroup ]]; then
    echo 'DinD needs --privileged, or privileged: true in Compose.' >&2
    exit 1
fi
if [[ $$ != 1 ]]; then
    echo 'DinD runs systemd as PID 1; drop --init, or set init: false in Compose.' >&2
    exit 1
fi

export DOCKER_HOST=unix:///var/run/docker.sock
export container=docker
mkdir -p /var/lib/docker
rm -f /run/team-worker-dockerd.pid
team-worker-ops systemd prepare "$@"
exec /sbin/init --system --unit=multi-user.target --log-level=warning

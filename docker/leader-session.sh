#!/usr/bin/env bash
set -uo pipefail
delay=${TEAM_WORKER_LEADER_RESTART_DELAY:-10}
if [[ ! $delay =~ ^[1-9][0-9]*$ ]]; then
    echo 'TEAM_WORKER_LEADER_RESTART_DELAY must be a positive number of seconds.' >&2
    exit 1
fi
trap 'exit 0' TERM INT HUP
while true; do
    if [[ -f /tmp/team-worker-provider-pending ]]; then
        echo 'Waiting for an API key in providers.yaml; fill it in and restart the container.' >&2
        sleep "$delay"
        continue
    fi
    "$@"
    status=$?
    echo "The leader exited with status $status; restarting in ${delay}s. Check the model credentials or the prompts in tmux." >&2
    sleep "$delay"
done

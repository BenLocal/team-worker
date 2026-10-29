#!/usr/bin/env bash
set -euo pipefail
mkdir -p "$HOME/.claude/skills" "$HOME/.codex/skills" "$TEAM_WORKER_DATA_DIR"
ln -sfn /opt/team-worker/skills/team-worker "$HOME/.claude/skills/team-worker"
ln -sfn /opt/team-worker/skills/team-worker "$HOME/.codex/skills/team-worker"
# Seed dsh's TUI profile once; later plugin changes and Magpie patches stay in the volume.
if [[ -d /opt/dsh/profiles/tui && ! -e $HOME/.dsh/profiles/tui ]]; then
    mkdir -p "$HOME/.dsh/profiles"
    seed=$(mktemp -d "$HOME/.dsh/profiles/.tui-seed.XXXXXX")
    cp -R /opt/dsh/profiles/tui/. "$seed"
    mv "$seed" "$HOME/.dsh/profiles/tui"
fi
# Keep credentials and CLI state in the persistent home volume.
if [[ ${1:-} != start ]]; then
    exec "$@"
fi
shift
if ! git rev-parse --show-toplevel >/dev/null 2>&1; then
    echo "Error: $PWD must be a Git repository; run git init on the host and make a commit." >&2
    exit 1
fi
if ! git rev-parse --verify HEAD >/dev/null 2>&1; then
    echo 'Error: the Git repository has no commits yet; make one on the host first.' >&2
    exit 1
fi
workspace=$(git rev-parse --show-toplevel)
cd "$workspace"
if [[ ! ${TEAM_WORKER_LEADER_RESTART_DELAY:-10} =~ ^[1-9][0-9]*$ ]]; then
    echo 'TEAM_WORKER_LEADER_RESTART_DELAY must be a positive number of seconds.' >&2
    exit 1
fi
# Apply the file on every start, including docker start on an existing container.
TEAM_WORKER_MODEL=$(team-worker-ops magpie \
    "${TEAM_WORKER_PROVIDERS_FILE:-/etc/team-worker/providers.yaml}")
export TEAM_WORKER_MODEL
if [[ -n $TEAM_WORKER_MODEL ]]; then
    unset ANTHROPIC_API_KEY ANTHROPIC_AUTH_TOKEN ANTHROPIC_BASE_URL ANTHROPIC_MODEL
fi
# Keep the browser sign-in stable on the same home volume.
key_file="$HOME/.config/magpie/web.key"
mkdir -p "$(dirname "$key_file")"
if [[ ! -s $key_file ]]; then
    (umask 077; python3 -c 'import secrets; print(secrets.token_hex(32))' > "$key_file")
fi
export MAGPIE_WEB_KEY
MAGPIE_WEB_KEY=$(cat "$key_file")
export MAGPIE_WEB_URL="${TEAM_WORKER_WEB_URL:-http://localhost:${TEAM_WORKER_WEB_PORT:-8080}}/magpie"
pids=()
cleanup() {
    trap - EXIT
    kill "${pids[@]}" 2>/dev/null || true
    tmux kill-server 2>/dev/null || true
    wait || true
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
magpie_pid=
if [[ ${TEAM_WORKER_DIND:-0} != 1 ]]; then
    magpie web --addr 127.0.0.1:3430 --no-open &
    magpie_pid=$!
    pids+=("$magpie_pid")
    # systemd mode runs openbao.service and openbao-agent.service instead.
    if [[ ${TEAM_WORKER_OPENBAO:-0} == 1 ]]; then
        mkdir -p -m 700 "$HOME/.openbao/data"
        bao server -config=/etc/openbao/openbao.hcl &
        pids+=($!)
        # Waits for manual unseal, so it stays out of the services that stop the container.
        team-worker-ops openbao \
            "${TEAM_WORKER_OPENBAO_FILE:-/etc/team-worker/openbao.yaml}" &
    fi
fi
ready=false
for ((i=0; i<150; i++)); do
    if magpie healthcheck >/dev/null 2>&1; then ready=true; break; fi
    if [[ -n $magpie_pid ]] && ! kill -0 "$magpie_pid" 2>/dev/null; then break; fi
    sleep 0.2
done
if [[ $ready != true ]]; then echo 'Magpie failed to start.' >&2; exit 1; fi
team-worker-ops plugins \
    "${TEAM_WORKER_PLUGIN_FILE:-/etc/team-worker/plugin.yaml}"
# Privacy masking is on by default; a failure is reported and retried next start.
team-worker-ops masking || echo 'Magpie privacy masking could not be turned on; retrying on the next start.' >&2
# Skill sources may need Git credentials that OpenBao Agent renders; with
# automatic unsealing nothing waits on a person, so wait for those files.
if [[ ${TEAM_WORKER_OPENBAO:-0} == 1 && ${TEAM_WORKER_OPENBAO_AUTO_UNSEAL:-0} == 1 ]]; then
    team-worker-ops openbao --wait-rendered 180s \
        "${TEAM_WORKER_OPENBAO_FILE:-/etc/team-worker/openbao.yaml}"
fi
team-worker-ops skills \
    "${TEAM_WORKER_SKILLS_FILE:-/etc/team-worker/skills.yaml}"
team-worker-ops mcp \
    "${TEAM_WORKER_MCP_FILE:-/etc/team-worker/mcp.yaml}"
team-worker-ops prompt \
    "${TEAM_WORKER_PROMPT_FILE:-/etc/team-worker/prompt.yaml}"
# The daemon runs in the foreground here so a crash stops the container;
# CLI autostart stays as a fallback for docker exec sessions.
if [[ -n ${TEAM_WORKER_API_KEY:-} ]]; then
    export TEAM_WORKER_HTTP_ADDR="${TEAM_WORKER_HTTP_ADDR:-127.0.0.1:8082}"
fi
team-workerd --data-dir "$TEAM_WORKER_DATA_DIR" &
pids+=($!)
ready=false
for ((i=0; i<50; i++)); do
    if TEAM_WORKER_NO_AUTOSTART=1 team-worker team discover >/dev/null 2>&1; then ready=true; break; fi
    sleep 0.2
done
if [[ $ready != true ]]; then echo 'team-workerd failed to start.' >&2; exit 1; fi
# Header links to the other panels Nginx serves on the same address.
board_links=(--link "Magpie=/magpie/")
if [[ ${TEAM_WORKER_OPENBAO:-0} == 1 ]]; then board_links+=(--link "OpenBao=/ui/"); fi
team-worker board serve --host 127.0.0.1 --port 8081 "${board_links[@]}" &
pids+=($!)
nginx -c /etc/nginx/team-worker.conf -g 'daemon off;' &
pids+=($!)
ready=false
for ((i=0; i<50; i++)); do
    if curl --noproxy '*' -fsS --max-time 1 http://127.0.0.1:8080/team-worker/ >/dev/null 2>&1; then
        ready=true; break
    fi
    sleep 0.2
done
if [[ $ready != true ]]; then echo 'The Web board failed to start.' >&2; exit 1; fi
export TEAM_WORKER_TEAM_NAME="${TEAM_WORKER_TEAM_NAME:-team-worker-docker}"
identity=$(team-worker-ops leader prepare "$TEAM_WORKER_TEAM_NAME")
read -r TEAM_WORKER_AGENT_NAME TEAM_WORKER_AGENT_ID <<< "$identity"
export TEAM_WORKER_AGENT_NAME TEAM_WORKER_AGENT_ID
export TEAM_WORKER_AGENT_TYPE=leader TEAM_WORKER_AGENT_LEADER=1 TEAM_WORKER_WORKSPACE_DIR="$workspace"
team=$TEAM_WORKER_TEAM_NAME
prompt="你是团队 $team 的 Leader，名字是 $TEAM_WORKER_AGENT_NAME。团队已注册，直接使用它。请使用 team-worker skill 完成用户任务。用 team-worker spawn tmux --team $team -n <名字> --task <任务> 创建 Worker（默认运行 claude）；Leader 和所有 Worker 都必须在本容器运行。收件箱消息、Worker 的进展和新的未分配 pending 任务会在你每轮结束时自动送到你面前（team-worker hook），你停在输入框时会收到一行提醒；不需要执行 task wait。收到任务通知时，检查任务列表，主动接手未分配的 pending 任务，按需拆分、分配并汇总结果到任务描述。工作目标以成员专属提示词和当前任务列表为准。"
# Use the CLI's shared bounded supervisor and exact-session persistence.
export TEAM_WORKER_START_BLOCKED_FILE=/tmp/team-worker-provider-pending
leader_delay=${TEAM_WORKER_LEADER_RESTART_DELAY:-10}
if [[ ! $leader_delay =~ ^[1-9][0-9]*$ ]]; then
    echo 'TEAM_WORKER_LEADER_RESTART_DELAY must be a positive number of seconds.' >&2
    exit 1
fi
leader_options=(--keepalive --max-restarts "${TEAM_WORKER_LEADER_MAX_RESTARTS:-3}" --restart-delay "${leader_delay}s")
saved_session=$(team-worker --json session show "$team" --agent "$TEAM_WORKER_AGENT_NAME" 2>/dev/null || true)
if [[ -n $saved_session ]] && python3 -c 'import json,sys; sys.exit(not bool(json.load(sys.stdin).get("sessionId")))' <<< "$saved_session"; then
    if [[ -n ${1:-} ]]; then leader_options+=(--task "$1"); fi
    team-worker agent resume "$team" "$TEAM_WORKER_AGENT_NAME" --replace "${leader_options[@]}"
else
    team-worker spawn tmux --team "$team" -n "$TEAM_WORKER_AGENT_NAME" --cwd "$workspace" \
        --no-skip-permissions "${leader_options[@]}" \
        --task "${1:-检查团队任务列表，认领并执行 pending 任务；没有任务时等待任务通知。}" \
        -- claude --append-system-prompt "$prompt" --allowedTools 'Bash(team-worker *)'
fi
team_session="team-worker-$team"
# A fixed session name lets the host script attach without knowing the team.
tmux new-session -d -s team-worker -t "$team_session"
team-worker-ops leader watch "$team" "$TEAM_WORKER_AGENT_NAME" &
pids+=($!)
# Detaching tmux keeps agents alive; a failed Web service stops the container.
set +e
wait -n "${pids[@]}"
status=$?
set -e
if [[ $status == 0 ]]; then status=1; fi
exit "$status"

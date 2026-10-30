# 会话恢复与 keepalive

> 原生 CLI 会话的保存、手动恢复、自动重启，以及恢复流程的验收脚本。

Agent 退出时，退出钩子会通知服务。如果 tmux 窗口是被直接关掉的，退出钩子来不及执行，服务每 10 秒检查一次进程，同样能发现。两种情况下，这个 Agent 手上 `in_progress` 的任务都会退回 `pending`，并通知 leader。

`spawn` 会保存原生 CLI 的会话信息，进程退出不删除这些信息。Claude 和 pi 在启动前分配明确的会话 ID；Codex 优先从启用的会话钩子获取 ID，未启用钩子时，在退出、查看或恢复时按工作目录和 Agent 身份匹配本地记录。匹配不唯一时会报错，不自动选择最近会话。其他 CLI 可用 `session save` 提供准确 ID。

```bash
team-worker session show my-team --agent alice
team-worker session save my-team --agent alice --session-id <原生会话ID>
team-worker agent resume my-team alice --dry-run
team-worker agent resume my-team alice

# keepalive 默认关闭；开启后在未经批准的退出后续接同一会话。
team-worker spawn tmux --team my-team -n alice --keepalive --max-restarts 3 --restart-delay 1s -- claude
```

恢复复用原 CLI 命令、工作目录和权限设置，并重新读取团队说明、成员提示词和当前任务。已有运行中的同名 Agent 时拒绝恢复，可用 `--replace` 明确替换。`agent resume` 支持 `--task`（或 `--task-file`）、`--keepalive`、`--max-restarts`、`--restart-delay` 覆盖保存的设置。

keepalive 适用于 tmux 和 subprocess，正常退出和异常退出均受重试上限约束；默认最多连续重启 3 次，等待时间逐次加倍、最长 1 分钟，一次运行达到 1 分钟后重置连续次数。原生会话 ID 缺失时停止恢复并报错。每轮进程使用独立 `run_id`，旧进程的退出回报不会修改新一轮的状态。退出时任务仍退回 pending，恢复后需重新检查任务归属并领取。

`agent stop`、批准 shutdown 和 `team cleanup` 会先保存停止意图，阻止后续重启。整个 tmux pane 或 supervisor 被结束后不会自动创建新 pane，可使用 `agent resume` 手动恢复。daemon 重启后已保存的会话仍可读取。

数据库只保存会话标识和启动信息，原生 CLI 的历史文件仍需保留。环境变量的值不随会话保存；通过模板或 `--env` 设置的变量，手动恢复时需在调用者环境或新的 `--env` 参数中提供。Claude、Codex、Gemini、Kimi、Qwen、OpenCode、pi 和 dsh 分别构造自己的精确会话恢复参数；具体可用性取决于安装的 CLI 版本，未知 CLI 不支持自动恢复。

恢复流程的可重复验收（使用隔离数据目录，不影响现有团队）：

```bash
make build
python3 scripts/verify-sessions.py --bin-dir bin
python3 scripts/verify-sessions.py --bin-dir bin --backend tmux
# 使用现有 Claude provider 配置，额外发起两次真实模型请求：
python3 scripts/verify-sessions.py --bin-dir bin --native-claude "$(command -v claude)"

# 其他原生 CLI 的真实会话验收，每次发起两次模型请求：
python3 scripts/verify-native-sessions.py --client codex --timeout 180 -- codex --skip-git-repo-check --json
python3 scripts/verify-native-sessions.py --client pi -- pi --no-tools --no-mcp --mode json --thinking off
python3 scripts/verify-native-sessions.py --client dsh -- dsh --profile headless --json
```

`verify-native-sessions.py` 检查准确会话 ID、新一轮 Run ID 和不调用工具的暗号回忆；日志保留在输出的临时目录。dsh 的会话 ID 从事件流读取后通过 `session save` 保存；如需复用网页保存的凭据或覆盖 headless 配置，在原生命令中追加本地 `--patch` 文件。

# Agent 会话恢复的参考实现问题
> 对照 ClawTeam 时需要注意的会话标识、退出清理和重启判定。

- 2026-10-29：源码核对本地 ClawTeam（BenLocal/ClawTeam，提交 `01198332ef9270c32c5460b8a178f964fc0df451`）发现，`clawteam/spawn/keepalive.py:86` 只对退出码 0 循环续接，使用 `--continue`、`resume --last` 等最近会话命令，无重试上限；它与指定 session ID 的手动恢复是两套路径。多个 Agent 共享工作目录时，最近会话不能作为准确匹配当前 Agent 的依据。
- 2026-10-29：上述 ClawTeam 的 `cli/commands.py:2954` 在 `lifecycle on-exit` 中无条件清除 `SessionStore` 记录，而 `run --resume` 在同文件 `4639` 行读取该记录。移植时应将原生会话标识与进程存活状态分开保存，退出不直接删除会话；此结论来自源码，未运行 ClawTeam 原生 CLI 恢复测试。
- 2026-10-29：team-worker 的逻辑 `AgentID` 在成员再次 spawn 时复用（`internal/cli/spawn.go`），而退出回报和 Reap 目前按 `(team, name)` 修改运行状态。引入自动恢复时需要独立的每轮运行标识，阻止旧进程迟到的退出事件作用于新进程；后续已用旧 Run ID 回报新运行的测试确认隔离行为，见 `internal/store/session_test.go` 和 `scripts/verify-sessions.py`。
- 2026-10-29：启用 tmux keepalive 后，pane 存活代表监督进程还在，不能据此判断原生 CLI 仍存活；子进程快速退出时应取消启动界面等待，否则会白等完整启动超时。现由 `child.Wait` 结束时取消 readiness context，Docker 内 tmux 进程级验收已覆盖快速失败和重试上限。
- 2026-10-29：`scripts/verify-sessions.py` 在隔离数据目录中启动真实 CLI、daemon 和子进程，验证两个后端的启动等待、有限重试、每轮独立 Run ID、迟到退出回报、主动停止与 daemon 重启后的会话保留；`--native-claude` 额外完成真实 Claude 初始对话及按准确 ID 续接后的暗号回忆。其余 CLI 原生续接尚未实测。
- 2026-10-29：更新容器时旧 Leader 记录指向已消失的 tmux pane，`kill-pane` 返回 `error connecting to ... (No such file or directory)`，阻断新 Leader 启动；`internal/tmux/tmux.go` 现将无 server/socket 与 pane 不存在都视为停止已完成，权限错误仍返回。`TestKillPaneAlreadyGone` 覆盖这几种回报；修复后实际重建容器，Leader 与看板已正常启动。

# 待办

- [x] team-worker CLI 支持 HTTP 远程调用及 API Key 鉴权。
  - CLI 可配置远程服务地址，通过 HTTP 调用 team-workerd。
  - HTTP 远程调用必须携带 API Key；服务端拒绝缺失或错误的 Key。
  - 本地 Unix socket 调用不需要 API Key，保持现有使用方式。
  - 验收覆盖有效 Key、缺失 Key、错误 Key，以及未配置 Key 的本地 socket 调用。
  - 已通过 `scripts/verify-http.py` 验证真实 CLI 的团队、任务、消息流程及免 Key socket；HTTP 模式不提供远程进程执行器。

## 后续需求

数据库迁移保障和 Agent 专属提示词持久化已实现。其余需求按以下建议顺序推进；下列已实现状态沿用现有记录。

### 1. 会话恢复（keepalive / resume）

已实现会话标识持久化、手动续接、有限重试、主动停止与旧退出事件隔离；Docker Leader 共用该流程。Claude 已有原生续接实测记录，2026-10-29 补测 Codex、dsh 和 pi，结果如下。

- [x] Codex（0.165.0）：在规范化工作目录下，通过 subprocess 启动及 `agent resume`，确认会话 ID 不变、Run ID 更新、不调用工具且准确回忆随机暗号。
- [x] pi（1.4.0）：通过 subprocess 启动及 `agent resume`，确认会话 ID 不变、Run ID 更新、不调用工具且准确回忆随机暗号。
- [x] 修复 dsh（0.2.1-rc.2）headless 的续接参数。headless 使用 `--session-id`，默认模式和 tui 保留 `--resume`；容器内实测 `agent resume` 续接同一会话、新 Run ID 和无工具暗号回忆通过。
- [x] 修复 Codex 自动捕获会话 ID 时的工作目录别名匹配。按文件系统身份识别同一目录；macOS 下传入 `/tmp/...`、原生记录为 `/private/tmp/...` 时，会话 ID 自动捕获、真实续接和无工具暗号回忆均通过。
- [x] OpenCode（1.18.29）自动捕获会话 ID：daemon 只读查询 `opencode.db`，按工作目录（含 `/var` 与 `/private/var` 别名）、Agent 身份和启动时间匹配顶层会话。2026-11-04 用真实 daemon 和 opencode 实测：会话 ID 自动保存，`agent resume --dry-run` 生成 `opencode run --format json --session <id>`。
- [ ] OpenCode 续接后的前文记忆：2026-11-04 本机没有可用模型（默认模型已停用，OpenCode Go key 过期，免费模型额度用完），待有模型后用验收脚本补测。Gemini、Kimi、Qwen 不再验证。

上述补测使用 `scripts/verify-native-sessions.py`；尚未逐个验证这些 CLI 的原生 tmux 交互与 keepalive 自动续接。

### 2. `plan` 审批

已实现，命令与 ClawTeam 兼容（`plan submit/approve/reject`），另有 `plan list/show`。

- [x] 明确计划批准前允许执行的操作：按成员开启（`spawn`/`team add-member --plan-required`）。开启后，成员在有已批准的计划前不能把任务改为 `in_progress` 或 `completed`，由服务端拒绝；查看任务、收发消息、提交计划不受限。未开启的成员不受影响。
- [x] 实现计划提交、批准、拒绝和审批状态管理：状态 pending → approved / rejected，只有 leader 可审批，已审批的计划不能再改；重新提交生成新的计划 ID。
- [x] 持久化计划正文与审批结果：`plans` 表（迁移 00007），看板 Approvals 页可查看。

### 3. `profile` / `preset`

已实现，命令、字段和 13 个内置预设与 ClawTeam 兼容，保存在 `~/.team-worker/config.json`。

- [x] 明确运行配置、端点预设与现有配置的关系及覆盖顺序：调用方环境 < 适配器 < profile < 模板与成员配置 < `spawn --env`；`spawn --dry-run` 列出每个变量来自哪一层。`spawn`、`launch` 和 `agent resume` 支持 `--profile`。
- [x] 确定凭据引用方式：只记录环境变量名（`api_key_env`、`env_map`），启动时从调用方环境读取，配置文件和输出中不出现密钥值。
- [x] 实现独立的运行配置与端点预设管理：`preset list/show/set/set-client/copy/remove/remove-client/generate-profile/bootstrap` 与 `profile list/show/set/remove/test`；交互式 `profile wizard` 和 `profile doctor` 见第 12 项。

### 4. `cost`

已实现，命令与 ClawTeam 兼容（`cost report/show/budget`），另有 `cost sync`。

- [x] 确定各 CLI 的用量来源与计价方式：`cost report` 手动上报 token 与费用；Agent 退出时和 `cost sync` 从 Claude Code、Codex 原生会话读取 token，只记 token、不计价（不维护价格表）。其他 CLI 只能手动上报。
- [x] 明确预算达到限制时仅告警还是阻止继续执行：默认告警，首次超出给 leader 发一条消息，看板 Spend 卡片标红；`cost budget --enforce` 开启后，超出预算拒绝新的任务领取和 spawn，进行中的任务仍可完成。
- [x] 实现用量统计、费用计算与预算管理：`cost_events` 表与团队预算字段（迁移 00009）。

### 5. `join` 审批

已实现，命令与 ClawTeam 兼容（`team request-join/join-status/approve-join/reject-join`），另有 `team join-requests`。

- [x] 实现加入申请与申请状态查询：申请存入 `join_requests` 表（迁移 00008），等待结果读取申请状态，不消费收件箱；同名的待审批申请不能重复提交。
- [x] 实现申请批准与拒绝流程：只有 leader 可审批；批准时在同一事务内加入成员并给新成员发 `join_approved`；未知申请 ID 报错。

### 6. `harness`

已实现，阶段状态保存在数据库（迁移 00010），看板 Board 页显示阶段进度。

- [x] 定义阶段、交付物、验收条件和失败处理：discuss（需 `discussion.md`）→ plan（需 `spec.md` 和已批准的规划者计划）→ execute（本轮任务全部完成）→ verify（`eval-report.json` 结论为 pass）→ ship（完成）。验证不通过时退回 execute 并生成返工任务，超过 `--max-attempts` 后整轮标记为失败。
- [x] 在所依赖能力就绪后，实现先规划后执行的阶段编排：`harness start/status/advance/approve/abort/artifact/artifacts/task/conduct`。`conduct` 每次检查都从数据库重新读取状态，按阶段拉起规划者、执行者、评审者，关卡满足后自动推进，中断后可接着运行。

## 与 ClawTeam 的差距

2026-10-30 对照 ClawTeam `120813f` 的 CLI 整理。以下是 ClawTeam 有、team-worker 尚未实现的能力，按建议顺序排列。消息唤醒（`runtime inject/watch`、`team watch`）、崩溃处理（`lifecycle should-keepalive/on-crash`）和浏览器终端（`board terminals`）已由 CLI hook、daemon 的消息粘贴（与 `runtime watch` 一样消息一到就粘贴，跑在 daemon 的 goroutine 里，不是单独的命令）、keepalive 和 Web 看板覆盖，不再单列。

### 7. `workspace`：Agent 的 git 工作区隔离

ClawTeam 为每个 Agent 建独立分支（worktree），team-worker 的 spawn 只创建普通目录 `<数据目录>/workspaces/<team>/<agent>`，分支与合并由 Agent 自行处理。多个 Agent 并行改同一仓库时需要这一项。2026-11-02 对照 ClawTeam `120813f` 定下方案（用户确认）：命令、参数和 `--json` 字段与 ClawTeam 一致，git 操作都在 daemon 所在的机器上执行。用法见 `docs/workspaces.md`。

- [x] 存储：迁移新增 `workspaces` 表，按 (team, agent, repo_root) 每个仓库一行，字段同 ClawTeam 的 `WorkspaceInfo`（`agent_name, agent_id, team_name, branch_name, worktree_path, repo_root, base_branch, created_at`）。
- [x] spawn / launch：`--workspace/--no-workspace`、可重复的 `--repo`；模式取配置 `workspace`（`auto`/`always`/`never`，环境变量 `TEAM_WORKER_WORKSPACE`，默认 `auto`）。本地模式 CLI 把自己的当前目录作为候选仓库，远程模式只认 `--repo`（daemon 机器上的路径）。分支 `clawteam/{team}/{agent}`，从仓库当前分支拉出；有残留时先清掉。spawn 失败回滚 worktree；prompt 写明目录、分支和完成前提交。
- [x] 多仓库：只有一个仓库时 `workspaces/{team}/{agent}` 就是 worktree（与 ClawTeam 一致）；多个仓库时它是普通目录，每个仓库一个以仓库目录名命名的 worktree 子目录（重名加序号），各仓库分支同名。
- [x] 团队或模板可配默认仓库列表，成员的 `--repo` 优先（`team spawn-team --repo`、`team repos`、模板 `repos`）。
- [x] `workspace list/status/checkpoint/merge/cleanup`：默认作用于成员的全部仓库，`--repo` 限定一个；只有一个仓库时输出与 ClawTeam 相同。
- [x] merge：先对每个仓库用 `git merge-tree --write-tree` 试合并，全部无冲突才逐个 `merge --no-ff`，否则一个都不合并并报告冲突的仓库和文件；主仓库有未提交改动时拒绝，合并后切回原分支；成功后默认清理 worktree 和分支。
- [x] 团队删除和 `team cleanup`：先 checkpoint 再删 worktree，**保留分支**（与 ClawTeam 不同，避免丢掉未合并的提交），删除表中记录。
- [x] 修正 ClawTeam 的已知问题：显式 `--workspace` 但不是 git 仓库时报错而不是静默降级；并发 spawn 不丢记录（SQLite）；merge 不留下切换过的主仓库分支。

### 8. `context`：跨 Agent 的改动上下文

依赖 `workspace`。算法和 JSON 字段照搬 ClawTeam。

- [x] 实现 `context diff/files/log`：各 Agent 分支相对基准的 diff 统计、文件归属（哪个 Agent 改了哪些文件）、提交记录。多仓库时文件写成“仓库名/路径”；`log` 用 NUL 分隔，避免提交信息含 `|` 时解析错误。
- [x] 实现 `context conflicts`：多个分支改动同一仓库的同一文件；改动行有交集为 `high`，否则 `medium`。
- [x] 实现 `context inject`：生成上述上下文的 Markdown，spawn 时放进初始 prompt（与 ClawTeam 一致）；另加 `--send` 作为消息发给该 Agent。

### 9. 团队快照

- [x] 实现 `team snapshot/snapshots/restore/snapshot-delete`：快照存在数据库的 `snapshots` 表（迁移 00015），保存团队配置与说明、预算、成员与提示词、任务与依赖、未读消息、事件、会话、计划和费用；不含进程记录、worktree 和 harness。保存和恢复各在一个事务里完成，daemon 在线时也一致；团队有运行中的 Agent 时拒绝恢复（409）。快照不随团队删除，可用来重建团队。用法见 `docs/collaboration.md`。

### 10. 事件 hook 与插件

team-worker 现有的 `hook` 子命令是给 Agent CLI 回调的，与 ClawTeam 的用户事件 hook 不是一回事，实现时需另起命名。

- [x] 定义事件类型，实现用户事件 hook 的增删查与测试：`event types`、`event hook list/add/remove/test`。共 12 种事件，能对上的沿用 ClawTeam 的名字，新增 `MemberJoined`、`MemberLeft`、`TaskCreated`、`BudgetExceeded`。事件和改动在同一个事务里写进 `event_outbox` 表（迁移 00016，只记录有 hook 订阅的），daemon 依次执行 shell hook 后再删除，重启后补执行。用法见 `docs/collaboration.md`。
- [x] 评估插件机制：不做。ClawTeam 的插件是进程内加载的 Python 类，唯一的内置插件 ralph loop 由 `spawn --keepalive` 覆盖，其他需求用事件 hook 加脚本实现。

### 11. MCP server

- [x] 评估把团队、任务、消息操作暴露为 MCP 工具（对应 `clawteam/mcp`）：不做（2026-11-04 决定）。成员 Agent 都能直接调用 `team-worker` CLI，`--json` 输出可供解析；远程操作走 `--remote`。

### 12. 配置与诊断

- [x] 实现 `config health`：本地模式检查数据目录能否写入、数据库能否以读写方式打开、socket 权限是否为 0600、daemon 是否应答；远程模式检查服务地址、Key 和服务端是否应答。不会启动 daemon，发现问题时退出码为 1，`--json` 字段名与 ClawTeam 一致。
- [x] 实现 `config show/set/get`：读写本机 `config.json` 的 `data_dir`、`user`、`server_url`、`workspace`、`default_profile`，来源标为 `env` / `file` / `default`，保留文件里的其他键。
- [x] 实现交互式 `profile wizard`（标准库逐行问答，不新增依赖）和 `profile doctor claude`（经 API 在 daemon 机器上改 `~/.claude.json`，文件不是合法 JSON 时拒绝而不是覆盖）。

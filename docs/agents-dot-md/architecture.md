# 系统架构

> 组件边界、关键链路、数据流与设计决策；动结构、加模块、接外部依赖前先看本文。

本文回答「这个系统由哪些部分组成、请求怎么流过它们、为什么这么切」。**技术选型和验证命令在 `tech-stack.md`**，两边别重复：这里写结构与取舍，那里写用了什么、怎么构建。

## 一、整体结构

```
team-worker (CLI, cobra)
  internal/cli ──> internal/client ──HTTP/JSON over Unix socket 或 HTTP──> team-workerd
                        │ 连不上时 startDaemon 后台拉起                │
                                                                     internal/server (net/http mux + Hub)
                                                                       └──> internal/store ──> SQLite (WAL, 单连接)

team-worker spawn / launch ──> internal/spawn ──> tmux 窗口 或 后台子进程（agent CLI）
                                                   └─ agent 退出时执行 `team-worker lifecycle on-exit` 回报服务
team-workerd --board ──> internal/board (只读 Web 看板，挂在 daemon 的 HTTP 端口，经进程内 client 读数据，经 proc/tmux 看终端)
team-worker board serve ──> internal/board (同一看板的独立进程，用于在另一台机器上查看远程 daemon)
```

- 部署形态：单机两进程，外加按需启动的 agent 进程（tmux 窗口或后台子进程）。`cmd/team-workerd` 是常驻服务，`cmd/team-worker` 是 CLI，二进制放在同一目录或都在 `PATH`。
- 进程边界：只有 `team-workerd` 打开数据库；CLI 经 socket 或带 API Key 的 HTTP 调 API，不直接读写 SQLite。同一数据目录靠 `team-workerd.lock`（flock）保证只有一个服务。
- Web 看板放在 daemon 里（`--board`，挂在 `--listen` 端口，`/v1/` 之外的路径）：少一个进程，看板和 API 版本始终一致。看板通过进程内 transport（`server.LocalClient`）调用 API，权限等同本机 socket；它缓冲完整响应，不能用于长轮询。实时终端的 goroutine 捕获 panic，只断开当前终端，不影响 daemon。`board serve` 保留给在另一台机器上查看远程 daemon 的场景。

## 二、模块与依赖方向

| 包 | 职责（一句话） | 允许依赖 | 禁止依赖 |
|---|---|---|---|
| `internal/model` | 共享数据类型与枚举校验，JSON 字段名沿用 ClawTeam | 标准库 | 其他 internal 包 |
| `internal/config` | 数据目录、socket 路径、`TEAM_WORKER_*` 环境变量与身份 | 标准库 | 其他 internal 包 |
| `internal/store` | 全部状态与业务规则（依赖解锁、任务锁、收件箱消费），事务内完成 | `model` | `server`、`client`、`cli` |
| `internal/server` | 把 store 暴露成 JSON API，长轮询，服务生命周期 | `store`、`model`、`config` | `client`、`cli` |
| `internal/client` | socket / 远程 HTTP 客户端；仅 socket 模式自动拉起服务 | `model`、`config`、`store`（仅复用 `*Input` / `TaskFilter` 类型） | `server`、`cli` |
| `internal/tmux` | 封装 tmux 命令：建窗口、发按键、粘贴、截屏 | 标准库 | 其他 internal 包 |
| `internal/proc` | 探测与停止 agent 进程（tmux / subprocess 两种后端） | `model`、`tmux` | `store`、`server` |
| `internal/agent` | Agent CLI 适配器的通用行为：启动参数、提示词投递、hook 配置、herdr 格式的画面规则引擎、用户 TOML 适配器；不写死任何具体 CLI | 标准库 + `BurntSushi/toml` | 其他 internal 包 |
| `internal/agent/builtin` | 内置适配器，每个 CLI 一个文件（claude、codex、pi 等），pi 扩展脚本和各 CLI 的画面规则（`detect/*.toml`，取自 herdr）也在这里；`Registry()` / `Load()` 是取适配器的入口 | `agent` | 其他 internal 包 |
| `internal/spawn` | 准备环境、按 CLI 适配器启动 agent，处理启动界面与 prompt 粘贴 | `tmux`、`agent`、`agent/builtin` | `store`、`server`、`client` |
| `internal/templates` | 读取 `~/.team-worker/templates` 下的 TOML 团队模板 | 标准库 + `BurntSushi/toml` | 其他 internal 包 |
| `internal/board` | 只读 Web 看板（页面 `embed` 进二进制） | `client`、`proc` | `store`、`server` |
| `internal/cli` | cobra 命令、人类可读输出与 `--json` | `client`、`model`、`config`、`spawn`、`agent`、`agent/builtin`、`proc`、`templates`、`board`、`store`（仅复用 `*Input` 类型） | `store` 的读写方法、`server`（测试除外） |

- 依赖方向硬规则：`cli → client → (HTTP) → server → store → model`，单向。进程相关能力（`spawn` / `proc` / `tmux`）由 CLI 进程直接使用；服务端只经 `store.Probe = proc.Probe` 注入进程探测，判断任务锁持有者是否已死。业务规则只写在 `store`，`server` 只做参数解析和错误码映射，`cli` 只做展示。
- `cli_test.go` 例外：用 `httptest` + `server.New(store)` 搭真实服务端做端到端测试。

## 三、分层约定

| 层 | 职责 | 不该做的事 |
|---|---|---|
| `server` handler | 解析路径 / query / body，调 store，`respond` 映射错误 | 写业务判断、拼 SQL |
| `store` 导出方法 | 校验输入，经 `s.write` 开事务，返回 `*store.Error`（Invalid / NotFound / Conflict） | 感知 HTTP |
| `store` 小写辅助函数 | 接收 `queryer`（`*sql.DB` 或 `*sql.Tx`），供事务内复用 | 自己开事务 |
| `cli` 命令 | 读 flag 与身份，调 client，`a.emit` 输出 | 绕过 client 直连数据库 |

错误码映射在 `server.respond`：Invalid→400、NotFound→404、Conflict→409、`context.Canceled`→503（客户端据此重试）。可选 TCP 监听在路由外校验 Bearer API Key，失败返回 JSON 401；socket 监听不经过鉴权，两者共用一个 Server、Hub 和 Store。

## 四、关键链路（端到端走一遍）

**Worker 完成任务，依赖它的任务解锁**
1. `team-worker task update <team> <id> -s completed`：`internal/cli/task.go` 的 `taskUpdateCmd`，身份取 `TEAM_WORKER_AGENT_NAME`。
2. `client.UpdateTask` 发 `PATCH /v1/teams/{team}/tasks/{id}`；socket 不存在或拒绝连接时 `startDaemon` 拉起服务再重试。
3. `server.updateTask` → `store.UpdateTask`：在一个事务里释放锁、记录 `duration_seconds`，`resolveDependents` 把所有前置都完成的任务从 `blocked` 改回 `pending`。
4. 提交后 `OnChange` → `Hub.Notify`，唤醒所有在等的长轮询请求。

**启动一个 agent（spawn）**
1. `team-worker spawn ...`（`internal/cli/spawn.go`）先准备原生会话命令、适配器和环境，再预登记本轮 Run ID 与会话元数据，最后启动 tmux 窗口或后台子进程并补齐进程信息，避免快速退出早于登记。
2. 普通运行的包装命令在退出时执行 `team-worker lifecycle on-exit --run-id ... --code ...`；启用 `--keepalive` 时，由隐藏的 `lifecycle supervise` 子进程管理启动界面、子进程退出和重试。
3. `store.AgentExitedRun` 仅接受当前 Run ID 的退出回报，将进行中任务退回 `pending` 并通知 Leader。每次重试分配新 Run ID；延迟到达的旧退出回报和旧会话钩子不会影响新运行。
4. `sessions` 按团队、成员、用户保存准确原生会话 ID、原始命令、工作目录与恢复策略，退出不删除。只记录额外环境变量的名称，手动恢复时重新提供值。Claude、pi 预分配 ID；启动钩子可捕获原生 ID；Codex 按工作目录与注入的 Agent ID 查找唯一会话，歧义时拒绝恢复。
5. keepalive 默认关闭，启用后连续快速退出默认最多重试 3 次，延迟指数增长至 1 分钟；运行满 1 分钟后重置连续失败计数。恢复重读团队上下文并提醒检查任务归属。主动 stop、替换、批准关闭和团队 cleanup 先写停止意图，重试登记在事务内校验旧 Run ID 与停止意图。
6. tmux 窗口被直接关掉、退出钩子没机会执行时，`team-workerd` 每 `ReapInterval`（10 秒，`internal/server/daemon.go`）用 `proc.Probe` 扫一遍运行中的 Agent，带 Run ID 回收。监督进程本身消失后不会由 daemon 重建；用 `agent resume` 手动续接。
7. 工作目录默认是 `<数据目录>/workspaces/<team>/<agent>`（`config` 里拼出），`--cwd` 可覆盖。

**长轮询收消息**
1. `inbox receive --wait` / `inbox watch` / `task wait` 调 `POST .../inboxes/{agent}/receive?wait=30s`。
2. `server.receive` 先取 `hub.Changed()` 再读库，避免读和等之间的写入被漏掉；没消息就阻塞到下次写入或超时（上限 `MaxWait` 60s）。
3. `store.Receive` 在一个事务里读出并删除消息，保证只投递一次。

## 五、数据流与存储

- 权威数据源：`<数据目录>/team-worker.db`，只有 `team-workerd` 写。团队模板是 `~/.team-worker/templates/*.toml` 文件，不进数据库。表：`teams`、`members`、`tasks`、`task_deps`、`messages`、`events`、`agents`、`sessions`、`settings`、`workspaces`。
- 团队公共提示词保存在 `teams.prompt`，成员专属提示词保存在 `members.prompt`，按 `(team, name, user_name)` 区分。模板渲染后的角色 `task` 和显式传入的 `spawn --task` 写入成员提示词；进程重新注册不覆盖它，启动和会话恢复钩子会重新读取。修改成员提示词与发送该成员的 inbox 通知在同一事务完成。
- 迁移：`internal/store/migrations/*.sql` 用 `go:embed` 内置，Goose Provider 负责顺序执行和事务，进度记在 `goose_db_version`。旧库的 `PRAGMA user_version` 一次性接入 Goose 历史；各 SQL 仍同步更新 `user_version`，用于版本兼容检查。**已发布的迁移不改，只追加文件。**
- 时间戳：字符串存储，格式对齐 Python `datetime.isoformat()`（`store.timestamp`），保持与 ClawTeam 输出一致。
- 通知：没有消息队列；`Hub` 是进程内「关闭旧 channel、换新 channel」的广播。

## 六、外部依赖与集成

默认通过本机 socket 访问；可选 HTTP 监听支持远程 API 调用，Key 通过环境变量提供。远程客户端不自动拉起 daemon，也不回退到本地 socket。spawn / proc / tmux 仍由运行 CLI 的机器执行。外部进程关系有两类：

- agent 进程：依赖 `tmux` 或直接起子进程，见上文 spawn 链路；`proc` 负责探测存活与停止。
- 服务进程：CLI 拉起 `team-workerd`，先找 CLI 同目录下的二进制，再查 `PATH`，`Setsid` 脱离终端，日志写 `team-workerd.log`，5 秒内健康检查不通则报错。

## 七、关键设计决策与取舍

- **单服务独占 SQLite，CLI 默认走 Unix socket**
  - 背景：ClawTeam 用一堆 JSON 文件，多进程并发写有竞态，消息可能重复投递。
  - 取舍：换来事务一致性和长轮询；代价是多一个常驻进程（用自动拉起缓解）。
  - 重新评估的信号：需要跨机器协作时。
- **`db.SetMaxOpenConns(1)`**：所有写串行，省掉锁冲突处理；单机负载下够用。
- **CLI 与 `--json` 字段兼容 ClawTeam**：已有的 agent prompt 和脚本不用改。改字段名或命令结构前先对照 ClawTeam。

## 八、扩展点

- 新增一个 API：`store` 加导出方法（事务走 `s.write`）→ `server.routes` 注册 → `client` 加方法 → `cli` 加命令；参照 `taskStats` 一路的实现。
- 新增一种 agent CLI：在 `internal/spawn/adapter.go` 加适配器（命令、环境变量、启动界面处理），参照已有适配器。
- `workspaces` 表（git worktree）已建好，Go 侧尚未接入。
- 明确禁止的改法：CLI 直接打开数据库；在 `server` 里写业务规则；修改已有迁移条目。

Web 与 CLI HTTP 共用 SQLite `settings` 中的访问 Key；daemon 启动时生成或保存配置覆盖值，Key 只允许本地 socket 读取。看板使用同一 Key 登录并签发带有效期的 HttpOnly cookie，保护页面、API 和 WebSocket；服务密钥仅在认证后按需读取，不随看板快照返回。

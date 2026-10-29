# 系统架构

> 组件边界、关键链路、数据流与设计决策；动结构、加模块、接外部依赖前先看本文。

本文回答「这个系统由哪些部分组成、请求怎么流过它们、为什么这么切」。**技术选型和验证命令在 `tech-stack.md`**，两边别重复：这里写结构与取舍，那里写用了什么、怎么构建。

## 一、整体结构

```
team-worker (CLI, cobra)
  internal/cli ──> internal/client ──HTTP/JSON over Unix socket──> team-workerd
                        │ 连不上时 startDaemon 后台拉起                │
                                                                     internal/server (net/http mux + Hub)
                                                                       └──> internal/store ──> SQLite (WAL, 单连接)

team-worker spawn / launch ──> internal/spawn ──> tmux 窗口 或 后台子进程（agent CLI）
                                                   └─ agent 退出时执行 `team-worker lifecycle on-exit` 回报服务
team-worker board serve ──> internal/board (只读 Web 看板，经 client 读数据，经 proc/tmux 看终端)
```

- 部署形态：单机两进程，外加按需启动的 agent 进程（tmux 窗口或后台子进程）。`cmd/team-workerd` 是常驻服务，`cmd/team-worker` 是 CLI，二进制放在同一目录或都在 `PATH`。
- 进程边界：只有 `team-workerd` 打开数据库；CLI 一律经 socket 调 API，不直接读写 SQLite。同一数据目录靠 `team-workerd.lock`（flock）保证只有一个服务。

## 二、模块与依赖方向

| 包 | 职责（一句话） | 允许依赖 | 禁止依赖 |
|---|---|---|---|
| `internal/model` | 共享数据类型与枚举校验，JSON 字段名沿用 ClawTeam | 标准库 | 其他 internal 包 |
| `internal/config` | 数据目录、socket 路径、`TEAM_WORKER_*` 环境变量与身份 | 标准库 | 其他 internal 包 |
| `internal/store` | 全部状态与业务规则（依赖解锁、任务锁、收件箱消费），事务内完成 | `model` | `server`、`client`、`cli` |
| `internal/server` | 把 store 暴露成 JSON API，长轮询，服务生命周期 | `store`、`model`、`config` | `client`、`cli` |
| `internal/client` | socket 上的 HTTP 客户端，自动拉起服务 | `model`、`config`、`store`（仅复用 `*Input` / `TaskFilter` 类型） | `server`、`cli` |
| `internal/tmux` | 封装 tmux 命令：建窗口、发按键、粘贴、截屏 | 标准库 | 其他 internal 包 |
| `internal/proc` | 探测与停止 agent 进程（tmux / subprocess 两种后端） | `model`、`tmux` | `store`、`server` |
| `internal/spawn` | 准备环境、按 CLI 适配器启动 agent，处理启动界面与 prompt 粘贴 | `tmux` | `store`、`server`、`client` |
| `internal/templates` | 读取 `~/.team-worker/templates` 下的 TOML 团队模板 | 标准库 + `BurntSushi/toml` | 其他 internal 包 |
| `internal/board` | 只读 Web 看板（页面 `embed` 进二进制） | `client`、`proc` | `store`、`server` |
| `internal/cli` | cobra 命令、人类可读输出与 `--json` | `client`、`model`、`config`、`spawn`、`proc`、`templates`、`board`、`store`（仅复用 `*Input` 类型） | `store` 的读写方法、`server`（测试除外） |

- 依赖方向硬规则：`cli → client → (HTTP) → server → store → model`，单向。进程相关能力（`spawn` / `proc` / `tmux`）由 CLI 进程直接使用；服务端只经 `store.Probe = proc.Probe` 注入进程探测，判断任务锁持有者是否已死。业务规则只写在 `store`，`server` 只做参数解析和错误码映射，`cli` 只做展示。
- `cli_test.go` 例外：用 `httptest` + `server.New(store)` 搭真实服务端做端到端测试。

## 三、分层约定

| 层 | 职责 | 不该做的事 |
|---|---|---|
| `server` handler | 解析路径 / query / body，调 store，`respond` 映射错误 | 写业务判断、拼 SQL |
| `store` 导出方法 | 校验输入，经 `s.write` 开事务，返回 `*store.Error`（Invalid / NotFound / Conflict） | 感知 HTTP |
| `store` 小写辅助函数 | 接收 `queryer`（`*sql.DB` 或 `*sql.Tx`），供事务内复用 | 自己开事务 |
| `cli` 命令 | 读 flag 与身份，调 client，`a.emit` 输出 | 绕过 client 直连数据库 |

错误码映射在 `server.respond`：Invalid→400、NotFound→404、Conflict→409、`context.Canceled`→503（客户端据此重试）。

## 四、关键链路（端到端走一遍）

**Worker 完成任务，依赖它的任务解锁**
1. `team-worker task update <team> <id> -s completed`：`internal/cli/task.go` 的 `taskUpdateCmd`，身份取 `TEAM_WORKER_AGENT_NAME`。
2. `client.UpdateTask` 发 `PATCH /v1/teams/{team}/tasks/{id}`；socket 不存在或拒绝连接时 `startDaemon` 拉起服务再重试。
3. `server.updateTask` → `store.UpdateTask`：在一个事务里释放锁、记录 `duration_seconds`，`resolveDependents` 把所有前置都完成的任务从 `blocked` 改回 `pending`。
4. 提交后 `OnChange` → `Hub.Notify`，唤醒所有在等的长轮询请求。

**启动一个 agent（spawn）**
1. `team-worker spawn ...`（`internal/cli/spawn.go`）调 `spawn.Launch`：按适配器拼命令和环境，`PATH` 首位放 team-worker 所在目录，用 tmux 窗口（会话名 `team-worker-<team>`）或后台子进程启动。
2. CLI 经 `POST /v1/teams/{team}/agents` 登记 agent，然后 `FinishStartup` 应答 agent CLI 的启动界面并粘贴 prompt。
3. agent 退出时包装命令执行 `team-worker lifecycle on-exit` → `POST .../agents/{name}/exit`，`store.AgentExited` 把它持有的进行中任务退回 `pending` 并通知 leader。
4. tmux 窗口被直接关掉、退出钩子没机会执行时，`team-workerd` 每 `ReapInterval`（10 秒，`internal/server/daemon.go`）用 `proc.Probe` 扫一遍运行中的 agent，按同样方式回收。
5. 工作目录默认是 `<数据目录>/workspaces/<team>/<agent>`（`config` 里拼出），`--cwd` 可覆盖。

**长轮询收消息**
1. `inbox receive --wait` / `inbox watch` / `task wait` 调 `POST .../inboxes/{agent}/receive?wait=30s`。
2. `server.receive` 先取 `hub.Changed()` 再读库，避免读和等之间的写入被漏掉；没消息就阻塞到下次写入或超时（上限 `MaxWait` 60s）。
3. `store.Receive` 在一个事务里读出并删除消息，保证只投递一次。

## 五、数据流与存储

- 权威数据源：`<数据目录>/team-worker.db`，只有 `team-workerd` 写。团队模板是 `~/.team-worker/templates/*.toml` 文件，不进数据库。表：`teams`、`members`、`tasks`、`task_deps`、`messages`、`events`、`agents`、`workspaces`。
- 迁移：`store.migrations` 是只追加的 SQL 列表，进度记在 `PRAGMA user_version`。**已发布的迁移不改，只追加新条目。**
- 时间戳：字符串存储，格式对齐 Python `datetime.isoformat()`（`store.timestamp`），保持与 ClawTeam 输出一致。
- 通知：没有消息队列；`Hub` 是进程内「关闭旧 channel、换新 channel」的广播。

## 六、外部依赖与集成

无网络依赖（`board serve` 除外，它在本机起一个 HTTP 看板）。外部进程关系有两类：

- agent 进程：依赖 `tmux` 或直接起子进程，见上文 spawn 链路；`proc` 负责探测存活与停止。
- 服务进程：CLI 拉起 `team-workerd`，先找 CLI 同目录下的二进制，再查 `PATH`，`Setsid` 脱离终端，日志写 `team-workerd.log`，5 秒内健康检查不通则报错。

## 七、关键设计决策与取舍

- **单服务独占 SQLite，CLI 走 Unix socket**
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

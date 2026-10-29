# team-worker

用 Go 重写的 [ClawTeam](https://github.com/HKUDS/ClawTeam) 多 Agent 协作工具，面向单机使用。仓库包含两个程序：

- `team-workerd`：常驻服务，独占一个 SQLite 数据库，在 Unix socket 上提供 HTTP/JSON API。团队、成员、任务和收件箱都存在这个库里。
- `team-worker`：命令行客户端，Agent 和人都通过它查任务、改状态、收发消息。命令结构沿用 ClawTeam 的 `team`、`task`、`inbox`，`--json` 输出的字段名与 ClawTeam 一致。

## 安装

需要 Go 1.26 或更新版本，以及 Node.js 22 或更新版本：Web 看板页面（`internal/board/web`，Vite 构建）在 `make build` 时生成并嵌入 `team-worker`，构建产物不入库。

```bash
make build     # 生成 bin/team-worker 和 bin/team-workerd
make install   # 安装到 GOBIN，未设置时为 GOPATH/bin
```

两个程序要放在同一目录，或都在 `PATH` 中。

## 启动服务

```bash
team-workerd                      # 前台运行，Ctrl+C 退出
team-workerd --data-dir /path     # 指定数据目录
```

不手动启动也可以：`team-worker` 第一次连不上服务时，会在后台自动拉起 `team-workerd`，日志写到数据目录的 `team-workerd.log`。设置 `TEAM_WORKER_NO_AUTOSTART=1` 可关闭自动拉起。同一个数据目录只能运行一个 `team-workerd`，第二个会报错退出。

## 使用

```bash
# 建团队，当前身份成为 leader；登记成员后才能收到广播
team-worker team spawn-team my-team -d "构建认证模块"
team-worker team add-member my-team alice --agent-type developer

# 建任务，--blocked-by 声明依赖
team-worker task create my-team "实现 OAuth2" -o alice -p high
team-worker task create my-team "编写单元测试" -o bob --blocked-by <任务ID>

# Worker 领取和完成任务；完成后，依赖它的任务自动从 blocked 变回 pending
TEAM_WORKER_AGENT_NAME=alice team-worker task update my-team <任务ID> -s in_progress
TEAM_WORKER_AGENT_NAME=alice team-worker task update my-team <任务ID> -s completed

# 消息
team-worker inbox send my-team leader "OAuth 完成"
team-worker inbox receive my-team --agent leader
team-worker inbox watch my-team --agent leader   # 阻塞监听，服务重启后自动重连

# Leader 等待全部任务完成
team-worker task wait my-team --timeout 3600
```

所有命令都支持 `--json`。`task update` 把任务改为 `in_progress` 时，会以调用者身份加锁；其他 Agent 再领取同一任务会报锁冲突，加 `--force` 可强制接管。

## 启动 Agent

```bash
# 默认用 tmux 后端和 claude；-- 后面换成任意 Agent CLI
team-worker spawn tmux --team my-team -n alice --agent-type developer --task "实现 OAuth2"
team-worker spawn tmux --team my-team -n bob --task "编写测试" -- codex --model gpt-5
team-worker spawn subprocess --team my-team -n carol --task "整理文档"   # 后台运行，输出写日志

team-worker agent list my-team            # 进程是否存活
team-worker agent screen my-team alice    # 查看终端当前内容
team-worker agent stop my-team alice      # 停止，手上的任务退回 pending
tmux attach -t team-worker-my-team        # 直接进入各 Agent 的窗口
```

`spawn` 会做这些事：

1. 登记成员。团队不存在时会先建团队，并把这个 Agent 设为 leader。
2. 创建工作目录 `<数据目录>/workspaces/<team>/<agent>`，用 `--cwd` 可以换成别的目录。目录里放什么由 Agent 自己决定，比如克隆仓库、生成文件。
3. 注入 `TEAM_WORKER_*` 身份变量。
4. 把身份、团队说明、任务和协作规则作为第一条提示词发给 Agent。

每种 CLI 怎样启动由**适配器**决定：加哪些权限参数（可以用 `--no-skip-permissions` 关闭）、提示词怎么传进去、需要哪些环境变量、启动时的确认画面怎么处理。项目内置了 claude、codex、gemini、kimi、qwen、opencode、dsh、pi 的适配器。其他 CLI，或者想调整内置适配器的行为，都可以在 `~/.team-worker/agents/<name>.toml` 里声明，不用改代码。环境变量按"调用者环境 → 适配器 → 模板 → `--env` → 身份变量"的顺序一层层合成，值里可以用 `${NAME}` 引用调用者的环境变量。完整规则见 [docs/agents.md](docs/agents.md)。

Claude Code 的 Agent 还会自动装上 team-worker 的 hook：它准备停下时，会先收到新消息、自己的待办任务，以及（对 leader 而言）团队的进展；干活途中也能收到消息；上下文被压缩后，会重新拿到团队信息。Agent 已经停在输入框时，hook 不会被触发；这时如果有未读消息，daemon 会往它的 tmux 终端里输入一行提醒，把它唤醒。这一机制对所有 tmux 后端的 CLI 都有效，也包括没有 hook 的 CLI。你在自己终端里启动的 Claude 会话也能用这套 hook：`claude --settings "$(team-worker hook settings --role leader)"`。详见 [docs/agents.md](docs/agents.md#hook让消息和任务自动送到-agent-面前)。pi 通过内置扩展接入同一套逻辑。和 Claude 不同，pi 停在输入框时，扩展会自己开启新的一轮，不需要往终端里打字提醒。Codex 的 hook 目前是实验性功能，默认关闭。

想在启动前确认会执行什么，用 `--dry-run`：它会打印选中的适配器、最终命令，以及每个环境变量的来源（不打印值），不会启动任何东西。

Agent 退出时，退出钩子会通知服务。如果 tmux 窗口是被直接关掉的，退出钩子来不及执行，服务每 10 秒检查一次进程，同样能发现。两种情况下，这个 Agent 手上 `in_progress` 的任务都会退回 `pending`，并通知 leader。

团队说明是所有 Agent 共用的信息，比如仓库位置、测试方法：

```bash
team-worker team prompt set my-team --file team-prompt.md   # 修改后成员会收到通知
team-worker team prompt show my-team
```

每个成员还可以保存自己的职责和工作要求，按团队、成员名和用户独立保存：

```bash
team-worker agent prompt set my-team alice --file reviewer.md
team-worker agent prompt show my-team alice
team-worker agent prompt set my-team alice ""   # 清空专属提示词
```

`set` 也接受直接传入文本或从 stdin 读取。`--user` 默认取当前用户，可指定其他用户的成员。修改后只有对应成员收到重新读取提示词的通知。团队说明和成员专属提示词都保存在 SQLite 中。每份成员专属提示词最多 65536 字节。

`spawn --task "..."` 会覆盖保存的专属提示词；未传 `--task` 时复用已有内容，显式传 `--task ""` 则清空。`launch` 会保存 TOML 中每个角色经过变量替换后的 `task`。会话恢复钩子会补回专属提示词和当前任务；`--dry-run` 不修改保存值。

## 模板

团队结构固定时，可以写一个 TOML 模板，用一条命令建好团队、任务依赖和所有 Agent：

```bash
team-worker launch my-template -g "给待办应用加上标签功能" --team-prompt-file team-prompt.md
```

项目不内置模板。模板放在 `~/.team-worker/templates/<name>.toml`，也可以直接传文件路径。格式、变量和示例见 [docs/templates.md](docs/templates.md)。

## 看板

```bash
team-worker board show my-team     # 终端里查看成员状态、任务和最近消息
team-worker board serve -p 8080    # 浏览器打开 http://127.0.0.1:8080，点成员可看实时终端
```

Web 看板是只读的，默认只监听 127.0.0.1。 `board serve --link NAME=URL`（可重复）在页面标题栏右侧显示到其他服务的链接，例如 `--link Docs=https://example.com`。

## Agent skill

[`skills/team-worker`](skills/team-worker/SKILL.md) 说明 leader 和 worker 怎样用这个 CLI 协作，以及 worker 的循环流程。安装时把整个目录复制或软链到 `~/.claude/skills/team-worker`（Claude Code）或 `~/.codex/skills/team-worker`（Codex）。

## 配置

| 变量 | 作用 | 默认值 |
|---|---|---|
| `TEAM_WORKER_DATA_DIR` | 数据目录 | 配置文件中的 `data_dir`，否则为 `~/.team-worker` |
| `TEAM_WORKER_SOCKET` | Unix socket 路径 | `<数据目录>/team-workerd.sock` |
| `TEAM_WORKER_AGENT_NAME` | 当前 Agent 名称，作为发件人、锁持有者和默认收件箱 | `agent` |
| `TEAM_WORKER_AGENT_ID` | 当前 Agent ID | 随机生成 |
| `TEAM_WORKER_USER` | 多用户场景下的用户名，收件箱按 `<user>_<name>` 区分 | 配置文件中的 `user` |
| `TEAM_WORKER_WORKSPACE_DIR` | spawn 为 Agent 设置的工作目录 | 由 spawn 设置 |
| `TEAM_WORKER_NO_AUTOSTART` | 设为任意值时不自动拉起服务 | 未设置 |

配置文件固定为 `~/.team-worker/config.json`，可设置 `data_dir` 和 `user`。Unix socket 路径不能超过约 100 个字符；数据目录路径较长时，用 `TEAM_WORKER_SOCKET` 把 socket 放到短路径下。

数据目录内容：

```
team-worker.db        SQLite 数据库（WAL 模式）
team-workerd.sock     API socket，权限 0600
team-workerd.lock     防止重复启动
team-workerd.log      自动拉起时的服务日志（按大小轮转，旧文件为 .1 到 .5）
team-workerd.out      自动拉起时的启动错误和崩溃输出
workspaces/<team>/<agent>/   spawn 创建的 Agent 工作目录
logs/<team>/<agent>.log      subprocess 后端的 Agent 输出
run/                  tmux 后端启动时临时写入的环境变量文件，读完即删
```

## 日志

team-workerd 记录这些事件：

- **状态变化**（info 级别）：团队创建和删除、成员加入、任务创建、任务的状态、负责人、锁或依赖发生变化（包括由谁修改）、团队说明更新、Agent 注册和退出、消息发送和广播。消息只记发件人、收件人、类型和字数，不记正文。
- **请求**：出错的请求按 warn 记录（5xx 按 error），带上错误信息；超过 2 秒的请求也按 warn 记为慢请求，长轮询（带 `wait=` 参数的请求）除外。其余请求按 debug 记录。
- **后台任务**：daemon 发现 Agent 已退出、往终端打字提醒空闲的 Agent。

| 参数 | 环境变量 | 默认 |
|---|---|---|
| `--log-level` | `TEAM_WORKER_LOG_LEVEL` | `info`（可选 `debug`、`warn`、`error`） |
| `--log-format` | `TEAM_WORKER_LOG_FORMAT` | `text`（可选 `json`） |
| `--log-file` | `TEAM_WORKER_LOG_FILE` | 空，写到 stderr |
| `--log-max-size` | `TEAM_WORKER_LOG_MAX_SIZE` | 单个文件达到 20 MB 时轮转 |
| `--log-max-files` | `TEAM_WORKER_LOG_MAX_FILES` | 保留 5 个轮转文件（`.1` 到 `.5`） |

由 CLI 自动拉起的 daemon 写日志到 `<数据目录>/team-workerd.log` 并按上表轮转；启动失败或崩溃时的输出单独写在 `team-workerd.out`。手动在前台运行 `team-workerd` 时，日志写到 stderr，由 systemd、Docker 等负责收集。

## API

服务只监听 Unix socket，请求和响应都是 JSON，出错时返回 `{"error": "..."}`。

| 方法和路径 | 说明 |
|---|---|
| `GET /v1/health` | 健康检查 |
| `GET/POST /v1/teams` | 列出 / 创建团队 |
| `GET/DELETE /v1/teams/{team}` | 团队详情 / 删除团队及其全部数据 |
| `POST /v1/teams/{team}/members` | 添加成员 |
| `GET /v1/teams/{team}/leader` | 查询 leader |
| `GET/POST /v1/teams/{team}/tasks` | 列出（`status`、`owner`、`priority`、`sort=priority`）/ 创建任务 |
| `GET /v1/teams/{team}/tasks/stats` | 任务统计 |
| `GET/PATCH /v1/teams/{team}/tasks/{id}` | 任务详情 / 更新任务 |
| `POST /v1/teams/{team}/messages` | 发消息 |
| `POST /v1/teams/{team}/broadcast` | 广播给除发件人外的所有成员 |
| `GET /v1/teams/{team}/messages/log` | 消息历史（`limit`、`from`） |
| `GET /v1/teams/{team}/inboxes/{agent}` | 查看收件箱但不消费 |
| `POST /v1/teams/{team}/inboxes/{agent}/receive` | 消费消息；`wait=30s` 时长轮询，有新消息立即返回 |
| `GET/PUT /v1/teams/{team}/prompt` | 读取 / 设置团队说明 |
| `GET/PUT /v1/teams/{team}/members/{name}/prompt?user=...` | 读取 / 设置成员专属提示词，`user` 默认空；设置时通知对应成员 |
| `GET /v1/teams/{team}/board` | 看板数据：成员及进程状态、任务、统计、最近消息 |
| `GET/POST /v1/teams/{team}/agents` | 列出 Agent（含实时存活检测）/ 登记已启动的 Agent |
| `GET /v1/teams/{team}/agents/{name}` | Agent 详情 |
| `POST /v1/teams/{team}/agents/{name}/exit` | 记录 Agent 退出，退回它手上的任务并通知 leader |

```bash
curl --unix-socket ~/.team-worker/team-workerd.sock http://x/v1/teams/my-team/tasks
```

## 与 ClawTeam 的差异

- 任务、消息不再是一个个 JSON 文件，统一存在 SQLite。依赖解锁、领取消息都在一个事务内完成，消息不会被重复投递。
- 不支持 ZeroMQ P2P 和 Redis 唤醒；长轮询由服务内部的写入通知触发。
- 对不存在的团队创建任务或发消息会报 `Team '<name>' not found`，ClawTeam 会静默创建目录。
- `task list` 按创建顺序返回，ClawTeam 按任务 ID 字母序。
- 环境变量前缀从 `CLAWTEAM_` 改为 `TEAM_WORKER_`。
- 不做 git worktree：每个 Agent 只拿到一个普通工作目录，怎么组织代码由团队自己决定。
- 不内置模板，团队结构完全由用户的模板定义。
- 团队说明和成员专属提示词保存在 SQLite 中，重启时复用；成员提示词支持单独修改并通知对应成员。
- 数据库按版本自动升级，旧库升级前自动备份；备份与恢复步骤见 [数据库升级与恢复](docs/agents-dot-md/environment.md#数据库升级与恢复)。
- 尚未实现：`profile`/`preset`、`cost`、`plan`、`join` 审批、`harness`、会话恢复（keepalive / resume）、Web 看板接管终端输入。

# 配置、远程调用与 API 参考

> 环境变量、数据目录、HTTP 远程调用、看板访问、日志参数和全部 API。

## 配置

全部环境变量（含 Docker 镜像和脚本用到的）汇总在 [environment-variables.md](environment-variables.md)。

| 变量 | 作用 | 默认值 |
|---|---|---|
| `TEAM_WORKER_DATA_DIR` | 数据目录 | 配置文件中的 `data_dir`，否则为 `~/.team-worker` |
| `TEAM_WORKER_SOCKET` | Unix socket 路径 | `<数据目录>/team-workerd.sock` |
| `TEAM_WORKER_SERVER_URL` | CLI 远程 HTTP(S) 服务地址；显式设为空字符串则使用本地 socket | 配置文件中的 `server_url`，否则未设置 |
| `TEAM_WORKER_API_KEY` | CLI HTTP 与 Web 共用的 Key；服务端设置时覆盖并保存到 SQLite；socket 不需要 | 服务端自动生成并保存在 SQLite，客户端按需提供 |
| `TEAM_WORKER_WEB_BASE_PATH` | 看板在反向代理下的公开路径前缀；Docker 为 `/team-worker` | 空 |
| `TEAM_WORKER_HTTP_ADDR` | daemon 的可选 HTTP 监听地址，可由 `--listen` 覆盖；启用时必须配置 Key | 未设置，不监听 TCP |
| `TEAM_WORKER_AGENT_NAME` | 当前 Agent 名称，作为发件人、锁持有者和默认收件箱 | `agent` |
| `TEAM_WORKER_AGENT_ID` | 当前 Agent ID | 随机生成 |
| `TEAM_WORKER_USER` | 多用户场景下的用户名，收件箱按 `<user>_<name>` 区分 | 配置文件中的 `user` |
| `TEAM_WORKER_WORKSPACE_DIR` | spawn 为 Agent 设置的工作目录 | 由 spawn 设置 |
| `TEAM_WORKER_WORKSPACE` | daemon 的工作区模式：`auto`、`always` 或 `never`，决定 spawn 是否给 Agent 建 git worktree（见 [workspaces.md](workspaces.md)） | 配置文件中的 `workspace`，否则 `auto` |
| `TEAM_WORKER_DEFAULT_PROFILE` | daemon 在 spawn 没指定 `--profile` 时用的运行配置（见 [profiles.md](profiles.md)） | 配置文件中的 `default_profile` |
| `TEAM_WORKER_NO_AUTOSTART` | 设为任意值时不自动拉起服务 | 未设置 |
| `TEAM_WORKER_START_BLOCKED_FILE` | keepalive 在该文件移除后才启动子进程；Docker 用于等待 provider Key 配置 | 未设置 |

配置文件固定为 `~/.team-worker/config.json`，可设置 `data_dir`、`user`、`server_url`、`workspace`、`default_profile` 和 `remotes`（见下文「HTTP 远程调用」里的操作员模式）。`server_url` 会让本机所有命令都走远程，此时 API Key 由环境变量提供；服务端默认值保存在 SQLite。Unix socket 路径不能超过约 100 个字符；数据目录路径较长时，用 `TEAM_WORKER_SOCKET` 把 socket 放到短路径下。

`team-worker config show|get|set` 读写本机这份配置文件里的 `data_dir`、`user`、`server_url`、`workspace` 和 `default_profile`，与 ClawTeam 的同名命令一致：`show` 列出每项的值和来源（`env` 表示来自环境变量、`file` 表示来自配置文件、`default` 表示默认值），`get KEY` 读一项，`set KEY VALUE` 写一项，`VALUE` 为空字符串时删除该项、恢复默认。环境变量优先于配置文件，所以设置了对应环境变量时，`set` 写入的值不生效。`set` 保留文件里的其他内容（`remotes`、`profiles`、`presets` 等），文件权限写为 600；`workspace` 只接受 `auto`、`always`、`never`。`workspace` 和 `default_profile` 由 daemon 读取，连远程服务时要在服务器上设置。

## 数据目录

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

## HTTP 远程调用

服务端启用 HTTP 监听后，使用 SQLite 中保存的共享 Key；首次启动自动生成，也可通过 `TEAM_WORKER_API_KEY` 指定并保存。原有 socket 同时保留：

```bash
team-workerd --listen 0.0.0.0:8082
```

在服务端本机用 `team-worker key show` 读取 Key，再为远程客户端设置同一个 `TEAM_WORKER_API_KEY`，指定服务地址后使用现有命令：

```bash
export TEAM_WORKER_SERVER_URL=http://server-host:8082
team-worker team discover
team-worker task list my-team
team-worker inbox send my-team leader "远程调用测试"

# 临时切回本地 socket，无需提供 Key：
TEAM_WORKER_SERVER_URL='' TEAM_WORKER_API_KEY='' team-worker team discover
```

`team-worker config health` 检查 CLI 依赖的东西，配置不对时先跑它。本地模式检查数据目录能否写入、数据库能否被当前用户以读写方式打开、socket 的权限是否为 0600，以及 daemon 是否应答；远程模式检查服务地址、是否设置了 Key，以及服务端是否应答。它不会启动 daemon：socket 文件还在但 daemon 已经退出时，报告 socket 不应答。发现问题时退出码为 1，`--json` 输出的字段名与 ClawTeam 的 `config health` 一致（`data_dir`、`writable`、`latency_ms`、`teams_count` 等），另有 `problems` 列出发现的问题。服务部署在反向代理的子路径下时，`TEAM_WORKER_SERVER_URL` 要带上这个路径（比如 `http://server-host:28088/team-worker-api`），否则请求会落到代理的其他位置。

team-workerd 是服务端，所有实际工作都由它完成；team-worker 只是客户端，走本地 socket 还是 HTTP 只是传输方式不同，命令的行为完全一样。启动成员（`spawn`、`launch`、`agent resume`、`harness conduct`）、停止成员（`agent stop`、`team cleanup`、`harness abort`）、看成员终端（`agent screen`）、读原生会话文件（`cost sync`、补抓 Codex 和 dsh 的会话 ID）都在 daemon 所在的机器上进行。成员继承 **daemon 的环境变量**，再叠加适配器、profile、模板和请求里的 `--env`；profile 和按名字引用的模板也来自 daemon 那台机器的 `~/.team-worker`。命令里的路径（`--cwd` 等）指 daemon 那台机器上的路径；`--task-file`、`--team-prompt-file` 和 `launch` 的模板文件例外，由客户端读出内容发过去。看板的实时终端只在看板跑在 daemon 那台机器上时可用（`team-workerd --board`）。

### 操作员模式：`--remote`

从团队外部操作某个部署时，把它登记在 `config.json` 的 `remotes` 里，命令前加全局参数 `--remote <名字>`，本机的其他命令不受影响：

```json
{
  "remotes": {
    "prod": {
      "server_url": "http://team-worker.example.internal:28088/team-worker-api",
      "api_key": "<Key>",
      "as": "operator"
    }
  }
}
```

```bash
team-worker --remote prod config health
team-worker --remote prod task list demo
```

- `server_url` 是完整的 API 地址，反向代理加了路径前缀时要带上（Docker 部署是 `/team-worker-api`）。
- 以 `as`（默认 `operator`）的身份发消息和建任务；本机的 Agent 身份变量（`TEAM_WORKER_AGENT_ID`、`TEAM_WORKER_TEAM_NAME` 等）会被忽略，在某个团队的 Agent 会话里执行也不会冒用那个成员的身份。
- 不自动启动本机 daemon；默认不走环境里的代理（服务器地址加入 `NO_PROXY`），需要代理时设 `"use_proxy": true`。
- `task wait` 不带 `--agent` 时读操作员自己的收件箱，不碰 Leader 的；`--task <ID>`（可重复）只等指定任务；查询临时失败会重试，连续 5 次失败才放弃。
- `config.json` 存了 Key 却对其他用户可读时，`config health` 会报为问题；文件权限应为 600。
- 鉴权只看 API Key：持有 Key 就能在服务器上启动、续接和停止成员，包括 `spawn ... -- <任意命令>`，所以 Key 要按服务器登录凭据的级别保管。

HTTP 请求使用 `Authorization: Bearer <API Key>`，包括健康检查在内的所有 API 均校验 Key；缺失或错误时返回 `401 {"error":"Unauthorized"}`。socket 请求按文件权限控制，不校验 Key。共享 Key 的读取接口仅在本地 socket 可用，已认证的远程 HTTP 请求也不能读取它。

远程模式不自动拉起本地 daemon，也不在连接失败后退回 socket。客户端拒绝携带凭据、query 或 fragment 的服务地址，并拒绝自动跟随重定向。HTTPS 可通过反向代理提供。

该配置切换 daemon API 的访问方式。`spawn`、`launch`、进程停止与终端操作仍在运行 CLI 的机器执行，本功能不提供远程进程执行器。

可重复验收（使用隔离数据目录、临时 Key 与本地随机端口）：

```bash
make build
python3 scripts/verify-http.py --bin-dir bin
```

## Web 看板访问

Web 看板由 team-workerd 直接提供：`--board`（环境变量 `TEAM_WORKER_BOARD=1`）让它在 `--listen` 的同一个端口上提供看板，`/v1/` 仍是 API，其他路径是看板，看板的数据在进程内读取。不开 `--board` 时 daemon 不提供网页。`team-worker board serve` 仍然保留，用于在另一台机器上连远程 daemon（设置 `TEAM_WORKER_SERVER_URL`，或用 `--remote <名字>`）查看：它是纯客户端，没有实时终端，各成员的画面由 daemon 读出后定时刷新显示。实时终端（接 tmux 窗口）只在 `team-workerd --board` 提供的看板上有。

Web 看板访问需使用与 CLI HTTP 调用相同的 Key，`board serve` 默认只监听 127.0.0.1；连远程 daemon 时，登录用的就是配置里这个远程的 API Key。默认 Key 自动生成并保存在 SQLite 中，重启复用；可在本机通过 `team-worker key show` 查看。页面登录后，`Service keys` 可查看 Magpie Web Key 和 OpenBao 登录 Token，默认遮挡并支持显示、复制。 `team-workerd --board-link NAME=URL` 或 `board serve --link NAME=URL`（可重复）在页面顶部显示到其他服务的链接按钮，例如 `--board-link Docs=https://example.com`；daemon 也可以用环境变量 `TEAM_WORKER_BOARD_LINKS`（逗号分隔）。反向代理把看板放在子路径下时，用 `TEAM_WORKER_WEB_BASE_PATH` 指定前缀，例如 `/team-worker`。

团队标题旁的 `Team prompt` 按钮显示团队公共提示词。每个 Agent 终端卡片的 `Prompt` 按钮和侧栏成员菜单的 `Show prompt` 显示该成员的专属提示词，未启动或已退出的成员也能查看。内容从 store 按需读取，未设置时显示空状态；弹窗支持长文本滚动及 Escape 关闭。

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

服务默认监听 Unix socket，也可用 `--listen` 增加需要 API Key 的 HTTP 监听。请求和响应都是 JSON，出错时返回 `{"error": "..."}`。

| 方法和路径 | 说明 |
|---|---|
| `GET /v1/health` | 健康检查 |
| `GET /v1/access-key` | 读取 CLI HTTP 与 Web 共用的 Key，仅限本地 socket |
| `GET/POST /v1/teams` | 列出 / 创建团队 |
| `GET /v1/overview` | 所有团队的概况：leader、成员数、任务数、未读消息数（`board overview` 用它） |
| `GET/DELETE /v1/teams/{team}` | 团队详情 / 删除团队及其全部数据 |
| `POST /v1/teams/{team}/members` | 添加成员 |
| `GET/POST /v1/teams/{team}/join-requests` | 列出（`status`）/ 提交加入申请 |
| `GET /v1/teams/{team}/join-requests/{id}` | 查询加入申请 |
| `POST /v1/teams/{team}/join-requests/{id}/approve` | 批准申请（仅 leader，请求体带 `caller`、可选 `assignedName`） |
| `POST /v1/teams/{team}/join-requests/{id}/reject` | 拒绝申请（仅 leader，请求体带 `caller`、可选 `reason`） |
| `GET /v1/teams/{team}/leader` | 查询 leader |
| `GET/POST /v1/teams/{team}/plans` | 列出（`agent`、`status`）/ 提交计划 |
| `GET /v1/teams/{team}/plans/{id}` | 查询计划 |
| `POST /v1/teams/{team}/plans/{id}/approve` | 批准计划（仅 leader，请求体带 `caller`、`agent`、可选 `feedback`） |
| `POST /v1/teams/{team}/plans/{id}/reject` | 拒绝计划（仅 leader，请求体同上） |
| `PUT /v1/teams/{team}/members/{name}/plan-required` | 打开或关闭成员的计划审批要求（`required`、`user`） |
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
| `POST /v1/teams/{team}/agents/{name}/exit?run_id=...&code=...` | 按运行标识回报退出并释放任务；无标识的旧回报只适用于旧运行记录 |
| `POST /v1/teams/{team}/agents/{name}/stop?run_id=...` | 保存该轮运行的停止意图 |
| `POST /v1/teams/{team}/agents/{name}/spawn` | 在 daemon 所在机器上启动成员（`agent spawn` 的全部参数，见 `model.SpawnRequest`），返回启动结果；`dryRun` 只检查不启动 |
| `POST /v1/teams/{team}/agents/{name}/resume` | 续接成员保存的原生会话（`model.ResumeRequest`：可覆盖 keepalive、重启次数、任务说明、`--env`、profile），`replace` 先停掉当前进程 |
| `POST /v1/teams/{team}/agents/{name}/terminate` | 停掉成员：补存会话 ID、登记停止意图（keepalive 不再重启）、结束进程、释放任务 |
| `GET /v1/teams/{team}/agents/{name}/screen?lines=N` | 成员终端（或 subprocess 日志）当前的内容 |
| `POST /v1/teams/{team}/costs/sync?agent=...` | 从 daemon 那台机器上的原生会话文件记录 token 用量 |
| `POST /v1/agents/restore` | 续接所有团队里没有停止意图、记录为运行中或中断退出（退出码 -1）、进程已不在、保存了会话的成员（daemon 启动时自动执行一次），返回每个成员的结果 `model.RestoredAgent` |
| `POST /v1/launch` | 按模板建团队并启动全部成员（`model.LaunchRequest`；`templateText` 是客户端读出的模板文件内容） |
| `GET/PUT /v1/profiles` | 读取 / 整体保存 daemon `config.json` 里的运行配置和端点预设（`profile`、`preset` 命令用它） |
| `POST /v1/profile-doctor/{client}` | 在 daemon 机器上修复客户端的本地状态，目前只支持 `claude`：在 `~/.claude.json` 里设置 `hasCompletedOnboarding`，保留其他内容 |
| `POST /v1/profiles/{name}/test` | 在 daemon 上用一句短提示运行一次该 profile（请求体 `prompt`、`cwd`），返回命令、退出码和输出 |
| `POST/DELETE /v1/teams/{team}/harness/conduct` | 让 daemon 开始 / 停止调度团队正在进行的 harness 运行，停止不影响这一轮本身 |
| `POST /v1/teams/{team}/agents/stop` | 批量保存团队的停止意图并返回进程记录 |
| `GET/PUT /v1/teams/{team}/sessions/{name}?user=...` | 读取 / 保存原生会话；保存时校验运行标识 |
| `PUT /v1/teams/{team}/repos` | 替换团队的默认仓库（请求体 `repos`，空数组清除）；路径必须是 daemon 机器上的 git 仓库，保存为主仓库根目录 |
| `GET /v1/teams/{team}/context/diff/{agent}?repo=...` | 成员相对基准分支的改动（`files_changed`、`insertions`、`deletions`、`diff_stat`、`commit_count`、`summary`） |
| `GET /v1/teams/{team}/context/files?repo=...` | 文件到修改过它的成员列表的映射 |
| `GET /v1/teams/{team}/context/conflicts?repo=...` | 被多个成员改过的文件（`file`、`agents`、`severity`） |
| `GET /v1/teams/{team}/context/log?repo=...&limit=50` | 各成员分支上的提交（`agent`、`hash`、`message`、`timestamp`、`files`），按时间倒序 |
| `GET /v1/teams/{team}/context/inject/{agent}?repo=...` | 给成员的上下文 Markdown，`{"context": "..."}` |
| `GET/PUT /v1/teams/{team}/workspaces?agent=...` | 列出团队（或一个 Agent）的 git worktree / 保存一条记录（daemon 的 spawn 用） |
| `DELETE /v1/teams/{team}/workspaces/{agent}?repo=...` | 删除一条 worktree 记录，不动磁盘 |
| `GET /v1/teams/{team}/workspaces/{agent}/status?repo=...` | 各 worktree 未提交改动的 diff 统计 |
| `POST /v1/teams/{team}/workspaces/{agent}/checkpoint` | 提交 worktree 里的全部改动（请求体 `repo`、`message`） |
| `POST /v1/teams/{team}/workspaces/{agent}/merge` | 全部试合并通过后合并回基准分支（`repo`、`target`、`noCleanup`）；拒绝时返回 `status: merge_failed` |
| `POST /v1/teams/{team}/workspaces/import` | 登记 ClawTeam workspace registry（请求体 `file`，默认 daemon 机器上该团队的）里仍然存在的 worktree |
| `POST /v1/teams/{team}/workspaces/cleanup` | 删除一个 Agent（`agent`）或整个团队的 worktree，保留有未合并提交的分支 |
| `GET/POST /v1/teams/{team}/snapshots` | 列出团队的快照（新的在前）/ 保存一份快照（请求体可带 `tag`） |
| `POST /v1/teams/{team}/snapshots/{id}/restore` | 用快照替换团队的状态（请求体 `dryRun` 为真时只返回数量）；团队有运行中的 Agent 时返回 409 |
| `DELETE /v1/teams/{team}/snapshots/{id}` | 删除快照 |
| `GET/POST /v1/event-hooks` | 列出全部事件 hook（按执行顺序）/ 添加一个（`event`、`command`、`team`、`priority`，`action` 只能是 `shell`） |
| `DELETE /v1/event-hooks?event=...&command=...` | 删除一个事件类型的 hook，带 `command` 时只删命令相同的；返回 `{"event": ..., "removed": N}` |
| `POST /v1/event-hooks/test` | 用虚构的事件（`event`、`team`、`agent`）立即执行匹配的 hook，返回事件内容和每个 hook 的退出码与输出 |
| `DELETE /v1/teams/{team}/sessions[/{name}]?user=...` | 删除一个 Agent 的会话，不带名字时删除整个团队的会话；返回 `{"cleared": N}` |
| `GET/POST /v1/teams/{team}/costs` | 用量汇总和明细（`agent` 只筛选明细）/ 上报一次用量 |
| `PUT /v1/teams/{team}/costs/sessions` | 记录一个原生会话的累计 token，按会话覆盖 |
| `GET/PUT /v1/teams/{team}/budget` | 读取 / 设置预算（`budgetCents`、`enforce`），读取时含已花费和是否已耗尽 |
| `GET/POST /v1/teams/{team}/harness` | 列出 harness 运行 / 开始一轮（`goal`、`cli`、`agentCount`、`maxAttempts`） |
| `GET /v1/teams/{team}/harness/{id}` | 运行状态和关卡；`{id}` 可写 `current`，表示正在运行或最新的一轮 |
| `POST /v1/teams/{team}/harness/{id}/advance` | 关卡满足时推进；评审失败时退回 execute |
| `POST /v1/teams/{team}/harness/{id}/approve` | 批准本轮规划者的待审批计划（仅 leader，请求体带 `caller`） |
| `POST /v1/teams/{team}/harness/{id}/abort` | 中止（请求体可带 `reason`） |
| `GET /v1/teams/{team}/harness/{id}/artifacts[/{name}]` | 交付物列表 / 内容 |
| `PUT /v1/teams/{team}/harness/{id}/artifacts/{name}` | 登记或覆盖交付物（`content`，最大 1 MiB） |
| `POST /v1/teams/{team}/harness/{id}/tasks` | 创建属于本轮当前尝试的任务 |
| `POST /v1/teams/{team}/harness/{id}/agents` | 记录为本轮启动的 Agent（`name`、`role`、`phase`） |

```bash
curl --unix-socket ~/.team-worker/team-workerd.sock http://x/v1/teams/my-team/tasks
```

Web 看板（`team-workerd --board` 或 `board serve`）的提示词读取接口：

| 接口 | 说明 |
|---|---|
| `GET /api/service-keys` | 登录后按需读取 Magpie Web Key 和 OpenBao 登录 Token，响应禁止缓存 |
| `GET /api/service-auth/magpie` | `TEAM_WORKER_MAGPIE_SESSION_LOGIN=1` 时给 Nginx `auth_request` 用：会话有效时返回 204，`X-Magpie-Key` 头带 Magpie Web Key；未开启返回 404 |
| `GET /api/openbao/login` | `TEAM_WORKER_OPENBAO_SESSION_LOGIN=1` 时用保存的 root 令牌签发 12 小时子令牌，包装 60 秒后跳到 OpenBao 界面自动登录；没有 `init.json` 时跳到 `/ui/`；未开启返回 404 |
| `GET /api/teams/{team}/prompt` | 查看团队公共提示词 |
| `GET /api/teams/{team}/members/{name}/prompt?user=...` | 查看成员专属提示词，`user` 默认空 |

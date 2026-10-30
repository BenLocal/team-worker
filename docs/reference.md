# 配置、远程调用与 API 参考

> 环境变量、数据目录、HTTP 远程调用、看板访问、日志参数和全部 API。

## 配置

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
| `TEAM_WORKER_NO_AUTOSTART` | 设为任意值时不自动拉起服务 | 未设置 |
| `TEAM_WORKER_START_BLOCKED_FILE` | keepalive 在该文件移除后才启动子进程；Docker 用于等待 provider Key 配置 | 未设置 |

配置文件固定为 `~/.team-worker/config.json`，可设置 `data_dir`、`user` 和 `server_url`。远程 CLI 通过环境变量提供 API Key；服务端默认值保存在 SQLite。Unix socket 路径不能超过约 100 个字符；数据目录路径较长时，用 `TEAM_WORKER_SOCKET` 把 socket 放到短路径下。

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

远程 CLI 可以执行所有命令。`spawn`、`launch`、`agent resume` 和 `harness conduct` 在客户端这台机器上启动 Agent，Agent 继承同样的服务地址和 Key；服务端记录每个 Agent 所在的主机（`host`），只探测、回收和提醒本机的 Agent，避免把另一台机器的 pane 编号当成本机进程。`agent screen` 和 `agent stop` 等进程操作只对本机的 Agent 生效；对其他机器上的 Agent 执行 `agent stop`、`team cleanup` 或 `harness abort` 时只登记停止请求，由那台机器上的 `--keepalive` 守护进程在几秒内结束它。看板的实时终端同样只覆盖看板所在机器上的 Agent。

HTTP 请求使用 `Authorization: Bearer <API Key>`，包括健康检查在内的所有 API 均校验 Key；缺失或错误时返回 `401 {"error":"Unauthorized"}`。socket 请求按文件权限控制，不校验 Key。共享 Key 的读取接口仅在本地 socket 可用，已认证的远程 HTTP 请求也不能读取它。

远程模式不自动拉起本地 daemon，也不在连接失败后退回 socket。客户端拒绝携带凭据、query 或 fragment 的服务地址，并拒绝自动跟随重定向。HTTPS 可通过反向代理提供。

该配置切换 daemon API 的访问方式。`spawn`、`launch`、进程停止与终端操作仍在运行 CLI 的机器执行，本功能不提供远程进程执行器。

可重复验收（使用隔离数据目录、临时 Key 与本地随机端口）：

```bash
make build
python3 scripts/verify-http.py --bin-dir bin
```

## Web 看板访问

Web 看板由 team-workerd 直接提供：`--board`（环境变量 `TEAM_WORKER_BOARD=1`）让它在 `--listen` 的同一个端口上提供看板，`/v1/` 仍是 API，其他路径是看板，看板的数据在进程内读取。不开 `--board` 时 daemon 不提供网页。`team-worker board serve` 仍然保留，用于在另一台机器上连远程 daemon（设置 `TEAM_WORKER_SERVER_URL`）查看：这时看板里的实时终端显示的是那台机器上运行的 Agent。

Web 看板访问需使用与 CLI HTTP 调用相同的 Key，`board serve` 默认只监听 127.0.0.1。默认 Key 自动生成并保存在 SQLite 中，重启复用；可在本机通过 `team-worker key show` 查看。页面登录后，`Service keys` 可查看 Magpie Web Key 和 OpenBao 登录 Token，默认遮挡并支持显示、复制。 `team-workerd --board-link NAME=URL` 或 `board serve --link NAME=URL`（可重复）在页面顶部显示到其他服务的链接按钮，例如 `--board-link Docs=https://example.com`；daemon 也可以用环境变量 `TEAM_WORKER_BOARD_LINKS`（逗号分隔）。反向代理把看板放在子路径下时，用 `TEAM_WORKER_WEB_BASE_PATH` 指定前缀，例如 `/team-worker`。

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
| `POST /v1/teams/{team}/agents/stop` | 批量保存团队的停止意图并返回进程记录 |
| `GET/PUT /v1/teams/{team}/sessions/{name}?user=...` | 读取 / 保存原生会话；保存时校验运行标识 |
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
| `GET /api/teams/{team}/prompt` | 查看团队公共提示词 |
| `GET /api/teams/{team}/members/{name}/prompt?user=...` | 查看成员专属提示词，`user` 默认空 |

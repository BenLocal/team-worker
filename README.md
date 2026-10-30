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

加入审批、计划审批、团队说明与成员提示词、用量与预算、阶段编排（harness）见 [docs/collaboration.md](docs/collaboration.md)。

## 启动 Agent

```bash
# 默认用 tmux 后端和 claude；-- 后面换成任意 Agent CLI
team-worker spawn tmux --team my-team -n alice --agent-type developer --task "实现 OAuth2"
team-worker spawn tmux --team my-team -n bob --task "编写测试" -- codex --model gpt-5
team-worker spawn subprocess --team my-team -n carol --task "整理文档"   # 后台运行，输出写日志
team-worker spawn tmux --team my-team -n dave --task-file task.md        # 较长的任务说明从文件读取

team-worker agent list my-team            # 进程是否存活
team-worker agent screen my-team alice    # 查看终端当前内容
team-worker agent stop my-team alice      # 停止，手上的任务退回 pending
tmux attach -t team-worker-my-team        # 直接进入各 Agent 的窗口
```

`spawn` 登记成员（团队不存在时先建团队并设为 leader），创建工作目录 `<数据目录>/workspaces/<team>/<agent>`（`--cwd` 可换），注入 `TEAM_WORKER_*` 身份变量，再把身份、团队说明、任务和协作规则作为第一条提示词发给 Agent。`--dry-run` 只打印选中的适配器、最终命令和每个环境变量的来源，不启动。Agent 退出或 tmux 窗口被关掉后，它手上 `in_progress` 的任务退回 `pending` 并通知 leader。

- 每种 CLI 怎样启动由适配器决定，内置 claude、codex、gemini、kimi、qwen、opencode、dsh、pi；自定义适配器、环境变量合成顺序、hook 和终端提醒见 [docs/agents.md](docs/agents.md)。
- 运行配置（profile）和端点预设（preset）见 [docs/profiles.md](docs/profiles.md)。
- 会话恢复与 keepalive 见 [docs/sessions.md](docs/sessions.md)。
- 团队结构固定时，可以用 TOML 模板一条命令建好团队、任务和所有 Agent：`team-worker launch my-template -g "..."`。项目不内置模板，格式见 [docs/templates.md](docs/templates.md)。

## 看板

```bash
team-worker board show my-team     # 终端里查看成员状态、任务和最近消息
team-workerd --listen 127.0.0.1:8080 --board   # daemon 同时提供 Web 看板，浏览器打开 http://127.0.0.1:8080
team-worker board serve -p 8080    # 另起一个看板进程，用于在别的机器上查看远程 daemon
```

Web 看板使用与 CLI HTTP 调用相同的 Key，本机用 `team-worker key show` 查看。访问、链接按钮和反向代理子路径的设置见 [docs/reference.md](docs/reference.md#web-看板访问)。

## 协作规则与 skill

被 `spawn` 启动的 Agent 在第一条提示词里收到协作规则：Leader 收到 Leader Protocol（拆任务、启动 Worker、跟进、审批、收尾），其他成员收到 Coordination Protocol 和 Worker Loop Protocol（领任务、汇报、待命、关闭）。规则写在 [`internal/spawn/prompt.md`](internal/spawn/prompt.md)，编译时嵌入二进制，改这个文件即可调整。

[`skills/teamworker`](skills/teamworker/SKILL.md) 用于从团队外部远程操作已部署的团队（查看看板、派任务、等结果）。

## 参考

- [docs/reference.md](docs/reference.md)：环境变量、数据目录、HTTP 远程调用、日志参数和全部 API。
- [docker/README.md](docker/README.md)：打包 Magpie、OpenBao 和 Nginx 的 Docker 部署。

后续需求见 [TODO.md](TODO.md)。

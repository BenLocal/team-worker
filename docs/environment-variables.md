# 环境变量一览

> team-worker 的 CLI、daemon、Agent、Docker 镜像和辅助脚本读取的全部环境变量，按读取方和设置位置分组。

本文汇总各处读取的环境变量，详细用法见各节链接的文档。只在测试里用到的变量不在此列。开关类变量除特别说明外，设为 `1` 表示开启，其他值或不设表示关闭。

## CLI 与 daemon 通用

`team-worker`（CLI）和 `team-workerd`（daemon）都读取这些变量。命令行参数优先于环境变量，环境变量优先于配置文件。

| 变量 | 作用 | 默认值 |
|---|---|---|
| `TEAM_WORKER_DATA_DIR` | 数据目录，存放 SQLite、socket、日志和 Agent 运行文件 | 配置文件的 `data_dir`，否则 `~/.team-worker`；Docker 镜像为 `/home/node/.team-worker` |
| `TEAM_WORKER_SOCKET` | daemon 的 Unix socket 路径 | `<数据目录>/team-workerd.sock` |
| `TEAM_WORKER_SERVER_URL` | CLI 连接的远程 HTTP(S) 地址；显式设为空字符串则强制使用本地 socket | 配置文件的 `server_url`，否则不设 |
| `TEAM_WORKER_API_KEY` | CLI HTTP 调用与 Web 看板共用的 Key；daemon 端设置时覆盖并保存到 SQLite；走 socket 时不需要 | daemon 自动生成并保存在 SQLite |
| `TEAM_WORKER_NO_AUTOSTART` | 设为任意非空值时，CLI 不自动拉起 daemon | 不设，第一次调用时自动拉起 |
| `TEAM_WORKER_USER` | 多用户场景下的用户名，收件箱按 `<user>_<name>` 区分 | 配置文件的 `user` |
| `TEAM_WORKER_WORKSPACE` | daemon 的工作区模式：`auto`、`always` 或 `never`，决定 spawn 是否为 Agent 建 git worktree（见 [workspaces.md](workspaces.md)） | 配置文件的 `workspace`，否则 `auto` |
| `TEAM_WORKER_DEFAULT_PROFILE` | spawn 未指定 `--profile` 时使用的运行配置（见 [profiles.md](profiles.md)） | 配置文件的 `default_profile` |
| `TEAM_WORKER_START_BLOCKED_FILE` | 带 keepalive 的 Agent 等该文件被删除后才启动子进程；Docker 用它等待 provider Key 配置好，daemon 开机续接的 Agent 也一样等 | 不设；Docker 入口脚本在拉起 daemon 前设为 `/tmp/team-worker-provider-pending` |

## daemon（team-workerd）

由 daemon 读取，括号里是对应的命令行参数。CLI 自动拉起 daemon 时，daemon 继承 CLI 进程的环境，所以 Docker 镜像把这些变量设为 ENV，无论哪个进程先拉起 daemon，配置都一样。

| 变量 | 作用 | 默认值 |
|---|---|---|
| `TEAM_WORKER_HTTP_ADDR` | HTTP 监听地址（`--listen`）；启用时必须有 Key | 不设，不监听 TCP；镜像为 `127.0.0.1:8082`，内置 Nginx 依赖它 |
| `TEAM_WORKER_BOARD` | 设为 `1` 时在 HTTP 端口上同时提供 Web 看板（`--board`） | 不设；镜像为 `1` |
| `TEAM_WORKER_BOARD_LINKS` | 看板标题栏的外部链接，`NAME=URL`，逗号分隔（`--board-link`，可重复） | 不设；镜像为 `Magpie=/magpie/,OpenBao=/openbao`，关闭 OpenBao 时入口脚本只保留 Magpie |
| `TEAM_WORKER_WEB_BASE_PATH` | 看板在反向代理下的公开路径前缀，不带末尾斜线 | 空；镜像为 `/team-worker` |
| `TEAM_WORKER_LOG_LEVEL` | 日志级别：`debug`、`info`、`warn`、`error`（`--log-level`） | `info` |
| `TEAM_WORKER_LOG_FORMAT` | 日志格式：`text` 或 `json`（`--log-format`） | `text` |
| `TEAM_WORKER_LOG_FILE` | 日志写入此文件并按大小轮转，代替 stderr（`--log-file`）；设置后 CLI 自动拉起 daemon 时不再传 `--log-file` | 不设；自动拉起时为 `<数据目录>/team-workerd.log` |
| `TEAM_WORKER_LOG_MAX_SIZE` | 日志文件达到多少 MB 时轮转（`--log-max-size`） | `20` |
| `TEAM_WORKER_LOG_MAX_FILES` | 保留几个轮转后的日志文件（`--log-max-files`） | `5` |
| `TEAM_WORKER_AUTO_REVIEW_COMMAND` | 请示 AI 自动审核运行的命令，按空格切分、不经过 shell；提示词从 stdin 传入，团队设了模型时追加 `--model <模型名>`，单次最长 5 分钟（见 [collaboration.md](collaboration.md#ai-自动审核)） | `claude -p --tools "" --no-session-persistence`（不给工具、不保存会话） |
| `TEAM_WORKER_MAGPIE_SESSION_LOGIN` | 设为 `1` 时开启 `GET /api/service-auth/magpie`：登录看板后进入 Magpie 不用再输 Web Key；未开启时该接口返回 404 | 不启用 |
| `TEAM_WORKER_OPENBAO_SESSION_LOGIN` | 设为 `1` 时开启 `GET /api/openbao/login`：用保存的 root 令牌签发 12 小时子令牌，自动登录 OpenBao 界面；未开启时该接口返回 404 | 不启用 |

## Agent 身份（spawn 注入）

spawn 启动 Agent 时把这些变量写进 Agent 的进程环境，Agent 里的 `team-worker` 命令据此知道自己是谁、属于哪个团队。一般不用手动设置。`--remote` 模式下 CLI 会忽略本地 Agent 的身份变量（`TEAM_WORKER_AGENT_NAME` 除外，它改用远程的发件人名），避免把本地身份带到远程部署。

| 变量 | 作用 | 未设置时 |
|---|---|---|
| `TEAM_WORKER_AGENT_NAME` | Agent 名称，用作发件人、锁持有者和默认收件箱 | `agent` |
| `TEAM_WORKER_AGENT_ID` | Agent ID | 随机生成 |
| `TEAM_WORKER_AGENT_TYPE` | Agent 类型 | `general-purpose` |
| `TEAM_WORKER_AGENT_LEADER` | 是否为团队 Leader（`1` / `0`） | 否 |
| `TEAM_WORKER_TEAM_NAME` | 所属团队 | 无 |
| `TEAM_WORKER_RUN_ID` | 本次运行的 ID，用于会话记录和 `agent` 生命周期命令的 `--run-id` | 无 |
| `TEAM_WORKER_WORKSPACE_DIR` | spawn 给 Agent 设定的工作目录（建了 worktree 时是 worktree 路径） | 无 |
| `TEAM_WORKER_BIN` | spawn 使用的 `team-worker` 可执行文件路径 | 无 |
| `TEAM_WORKER_PLAN_MODE_REQUIRED` | 用 `--plan-required` spawn 的 Agent 会设为 `1`（也接受 `true`、`yes`），表示要先提交计划、经 Leader 批准再动手 | 否 |
| `TEAM_WORKER_DATA_DIR`、`TEAM_WORKER_SOCKET`、`TEAM_WORKER_USER` | 同上文通用变量，spawn 按 daemon 的设置写入 | — |

spawn 还会按 Agent 类型设置所用 CLI 的变量：以跳过权限确认的方式启动 dsh 时设置 `DSH_PERMISSION_MODE=danger-full-access`。

## 收件箱 `--exec` 回调

`team-worker inbox watch --exec <命令>` 收到消息时，以这些变量把消息交给回调命令：

| 变量 | 内容 |
|---|---|
| `TEAM_WORKER_MSG_FROM` | 发件人 |
| `TEAM_WORKER_MSG_TO` | 收件人 |
| `TEAM_WORKER_MSG_CONTENT` | 正文 |
| `TEAM_WORKER_MSG_TYPE` | 消息类型 |
| `TEAM_WORKER_MSG_TIMESTAMP` | 时间戳 |
| `TEAM_WORKER_MSG_JSON` | 整条消息的 JSON |

## 读取的第三方 CLI 目录

team-worker 读取 Agent CLI 的会话和用量文件时，按这些变量找目录：

| 变量 | 用途 | 默认值 |
|---|---|---|
| `HOME` | 各工具目录的基准，也决定 `~/.team-worker` 等默认路径 | 系统设置；镜像为 `/home/node` |
| `XDG_CONFIG_HOME` | Magpie Web Key（`magpie/web.key`）等配置文件所在目录 | `~/.config` |
| `CLAUDE_CONFIG_DIR` | Claude Code 的配置目录，读取用量 | `~/.claude` |
| `CODEX_HOME` | Codex 的目录，读取会话和用量 | `~/.codex` |
| `DSH_HOME` | dsh 的目录，读取会话 | `~/.dsh` |
| `XDG_DATA_HOME` | OpenCode 数据库（`opencode/opencode.db`）所在目录，读取会话 | `~/.local/share` |

## Docker 容器

这些变量在容器启动时传入（`docker run -e`、compose 的 `environment:`、`TEAM_WORKER_ENV_FILE` 指定的 env 文件），由入口脚本、`team-worker-ops` 和 systemd 单元读取。systemd 模式下，`team-worker-ops systemd prepare` 把容器环境写进 `/run/team-worker/agent.env`，各服务、Leader 和成员都继承它。

### 模型

| 变量 | 作用 | 默认值 |
|---|---|---|
| `TEAM_WORKER_MODEL` | Magpie 模型路由，优先于 `providers.yaml` 的 `claude.model`；入口脚本把解析结果写回这个变量 | `providers.yaml` 的配置 |
| `LLM_API_BASE` | 默认 `providers.yaml` 里 OpenAI 兼容网关的地址（`url_env`） | 无，使用默认配置时必须提供 |

### 服务开关

| 变量 | 作用 | 默认值 |
|---|---|---|
| `TEAM_WORKER_DIND` | 设为 `1` 时以 systemd 模式运行并启用 Docker in Docker，需要 `--privileged` | 不启用 |
| `TEAM_WORKER_MASKING` | home 卷首次启动时打开 Magpie 的 Privacy masking；设为 `0` 跳过这一步 | 打开 |
| `TEAM_WORKER_OPENBAO` | 设为 `1` 时启动 OpenBao 服务端和 OpenBao Agent；`0` 时都不启动，看板也不显示 OpenBao 链接 | `1`（镜像设定） |
| `TEAM_WORKER_OPENBAO_AUTO_UNSEAL` | 设为 `1` 时首次启动自动初始化、之后每次自动解封，解封密钥和 root 令牌存在 `~/.openbao/init.json`；`0` 时由人工初始化和解封 | `1`（镜像设定） |
| `TEAM_WORKER_DESKTOP` | 设为 `1` 时启动虚拟桌面（Xvfb、D-Bus 会话和 AT-SPI、Openbox），成员用 agent-desktop 操作其中的图形程序；`0` 时不启动 | `1`（镜像设定） |
| `TEAM_WORKER_MAGPIE_SESSION_LOGIN` | 设为 `1` 时登录看板即可进入 Magpie；没有看板会话时跳到看板登录页 | 不启用 |
| `TEAM_WORKER_OPENBAO_SESSION_LOGIN` | 设为 `1` 时从看板点 OpenBao 自动登录 OpenBao 界面；需要自动初始化留下的 root 令牌，人工模式仍手动登录 | 不启用 |

两个会话登录开关由入口脚本在启动 Nginx 前处理（把 `/etc/nginx/nginx-*-session.conf` 放进 `/tmp/team-worker-nginx/`），daemon 也会检查同名变量；改开关后要重启容器。详见 [docker/README.md](../docker/README.md) 的《一个端口访问多个面板》。

### 配置文件路径

容器内读取的配置文件，变量指定的是容器内路径：

| 变量 | 文件 | 默认值 |
|---|---|---|
| `TEAM_WORKER_PROVIDERS_FILE` | Magpie 模型供应商（格式见 [providers.md](providers.md)） | `/etc/team-worker/providers.yaml` |
| `TEAM_WORKER_SKILLS_FILE` | 要安装的 skill | `/etc/team-worker/skills.yaml` |
| `TEAM_WORKER_MCP_FILE` | MCP 服务器 | `/etc/team-worker/mcp.yaml` |
| `TEAM_WORKER_PROMPT_FILE` | 团队提示词 | `/etc/team-worker/prompt.yaml` |
| `TEAM_WORKER_PLUGIN_FILE` | Magpie 插件 | `/etc/team-worker/plugin.yaml` |
| `TEAM_WORKER_OPENBAO_FILE` | 要导入 OpenBao 的 KV 密钥和要渲染的文件 | `/etc/team-worker/openbao.yaml` |
| `TEAM_WORKER_OPENBAO_TOKEN_FILE` | 导入 OpenBao 用的令牌；自动解封且没有这个文件时改用 `init.json` 里的 root 令牌 | `/etc/team-worker/openbao.token` |

### Web 地址与代理

| 变量 | 作用 | 默认值 |
|---|---|---|
| `TEAM_WORKER_WEB_URL` | 浏览器访问容器的外部地址，不带末尾斜线；用于打印链接和生成 `MAGPIE_WEB_URL` | `http://localhost:<TEAM_WORKER_WEB_PORT>` |
| `TEAM_WORKER_WEB_PORT` | 宿主机发布的端口，`TEAM_WORKER_WEB_URL` 未设置时用来拼地址 | `8080` |
| `MIRROR_HTTP_PROXY` | 只给安装步骤用的代理：`team-worker-ops` 安装 Magpie 插件和 MCP 服务器时，用它覆盖子进程的 `HTTP_PROXY` / `HTTPS_PROXY`，Agent 不受影响 | 无 |
| `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`（及小写形式）、`ALL_PROXY` | 代理；`NO_PROXY` 建议包含 `localhost,127.0.0.1,::1` | 无 |

### 镜像固定设置

Dockerfile 用 ENV 设好、一般不需要改的变量：

| 变量 | 值 | 作用 |
|---|---|---|
| `HOME` | `/home/node` | node 用户的家目录，即 home 卷 |
| `TERM` | `xterm-256color` | tmux 和 Agent 终端的颜色支持 |
| `PATH` | 前置 `/opt/team-worker/bin:/opt/venv/bin` | team-worker 和 Python 工具 |
| `MAGPIE_ADDR` | `127.0.0.1:3425` | 容器内 Agent 使用的 Magpie 模型网关地址 |
| `MAGPIE_BUN` | `/usr/local/bin/bun` | Magpie 插件运行时 |
| `DISABLE_AUTOUPDATER` | `1` | 关闭 Claude Code 自动更新，版本随镜像固定 |
| `NARB_DISABLE_NATIVE_CACHE` | `1` | 让 dsh 直接从 `node_modules` 加载原生扩展，不复制到 `/tmp`（systemd 模式下 `/tmp` 是 noexec） |
| `PLAYWRIGHT_BROWSERS_PATH` | `/opt/ms-playwright` | 预装 Chromium 的位置 |
| `DISPLAY` | `:99` | 虚拟桌面的 X 显示，成员启动的图形程序显示在这里 |
| `DBUS_SESSION_BUS_ADDRESS` | `unix:path=/tmp/team-worker-desktop/bus` | 虚拟桌面的 D-Bus 会话总线，agent-desktop 经它的 AT-SPI 读取界面 |
| `SAL_USE_VCLPLUGIN` | `gtk3` | 让 LibreOffice 用 GTK3 界面，agent-desktop 才能读到它的控件 |
| `LANG` | `C.UTF-8` | 程序按 UTF-8 读取中文文件名和窗口标题；不设时是 POSIX，LibreOffice 会把中文文件名显示成乱码 |

此外，入口脚本运行时设置 `MAGPIE_WEB_KEY`（从 `~/.config/magpie/web.key` 读取，首次启动时生成）和 `MAGPIE_WEB_URL`（`<TEAM_WORKER_WEB_URL>/magpie`），供 Magpie 和 `team-worker-ops` 使用。

## 宿主机脚本 `docker/team-worker.sh`

在宿主机上运行脚本时读取：

| 变量 | 作用 | 默认值 |
|---|---|---|
| `TEAM_WORKER_IMAGE` | 构建和启动用的镜像名 | `team-worker-local:latest` |
| `TEAM_WORKER_CONTAINER` | 容器名 | `team-worker` |
| `TEAM_WORKER_ENV_FILE` | 以 `--env-file` 传给容器的文件，放代理、`LLM_API_BASE` 等 | 无 |
| `TEAM_WORKER_WEB_BIND` | 发布端口绑定的宿主机地址 | `127.0.0.1` |
| `TEAM_WORKER_WEB_PORT` | 发布到宿主机的端口 | `8080` |
| `TEAM_WORKER_BIN_DIR` | 二进制目录：`build` 的产物位置；`start` 时目录里有 `team-worker` 就只读挂载并优先使用 | 仓库的 `bin/docker` |
| `TEAM_WORKER_CONFIG_DIR` | 配置目录，从中挂载 `providers.yaml`、`skills.yaml`、`prompt.yaml`、`mcp.yaml`、`plugin.yaml`，以及可选的 `openbao.yaml`、`openbao.token`、`gitconfig` | `docker/magpie-config` |
| `TEAM_WORKER_PROVIDERS_FILE`、`TEAM_WORKER_SKILLS_FILE`、`TEAM_WORKER_PROMPT_FILE` 等 | 宿主机上的单个配置文件，优先于配置目录里的同名文件 | 配置目录里的文件 |

`start` 会把宿主机上已设置的以下变量原样传进容器：`ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_BASE_URL`、`TEAM_WORKER_MODEL`、`TEAM_WORKER_WEB_URL`、`TEAM_WORKER_API_KEY`、`TEAM_WORKER_HTTP_ADDR`、`TEAM_WORKER_OPENBAO`、`TEAM_WORKER_OPENBAO_AUTO_UNSEAL`、`TEAM_WORKER_MAGPIE_SESSION_LOGIN`、`TEAM_WORKER_OPENBAO_SESSION_LOGIN`、`TEAM_WORKER_DESKTOP` 和各代理变量。`TEAM_WORKER_DIND=1` 时以 privileged、systemd 模式启动。Magpie 接管模型路由时，入口脚本会清掉 `ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_BASE_URL`、`ANTHROPIC_MODEL`。

## 镜像构建参数

`./docker/team-worker.sh build` 把同名环境变量作为 Docker 构建参数传入（见 [docker/README.md](../docker/README.md) 的《构建参数》）：

| 变量 | 作用 |
|---|---|
| `CLAUDE_VERSION`、`CODEX_VERSION`、`PI_VERSION` | Agent CLI 的 npm 版本 |
| `DSH_VERSION`、`DSH_TUI_VERSION` | dsh 与 dsh-tui 的 npm 版本 |
| `MAGPIE_VERSION` | Magpie 镜像 tag |
| `OPENBAO_VERSION` | OpenBao 版本 |
| `BUN_VERSION`、`PNPM_VERSION`、`PLAYWRIGHT_VERSION`、`DOCKER_VERSION`、`AGENT_DESKTOP_VERSION` | 其他工具版本 |
| `DEBIAN_MIRROR`、`NPM_REGISTRY`、`PIP_INDEX_URL`、`BUN_DOWNLOAD_BASE`、`PLAYWRIGHT_DOWNLOAD_HOST` | 下载源，国内网络可换成镜像源 |
| `GOPROXY` | 宿主机编译二进制时的 Go 模块代理 |
| `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` | 构建时的代理；代理在本机回环地址时脚本自动加 `--network host` |

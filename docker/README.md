# team-worker Docker 镜像

team-worker、tmux、Git、Node.js、Claude Code、Codex、pi、DeepSeek Harness（dsh 与 dsh-tui）、Magpie、OpenBao、Nginx 和 Docker 工具打包在同一个容器里，宿主机只需要 Docker 和 Bash。Leader 在容器内 tmux 会话 `team-worker-<团队>` 的 `leader` 窗口运行；Leader 用 `team-worker spawn tmux` 创建的 Worker 作为同一会话的其他窗口，也在本容器内运行。

本目录参照 ClawTeam-Docker 改写：ClawTeam 换成本仓库的 team-worker（镜像构建时从源码编译），Magpie、OpenBao、Nginx、DinD/systemd 等组件和配置方式保持不变。容器内的启动配置由一个 Go 程序 `team-worker-ops` 完成（源码在 [`ops/`](ops/)），取代原来的 Python 脚本。

```bash
./docker/team-worker.sh build
# 项目必须是有至少一次提交的普通 Git 仓库（不是宿主机的 linked worktree）。
TEAM_WORKER_ENV_FILE=/绝对路径/team-worker.env \
  ./docker/team-worker.sh start /绝对路径/你的项目 "创建团队，实现登录功能并验证"
```

先编辑 `docker/magpie-config/providers.yaml`，或用 `TEAM_WORKER_PROVIDERS_FILE=/path/to/providers.yaml` 指定文件。默认配置通过环境变量 `LLM_API_BASE` 读取 OpenAI 兼容网关地址，所以 env-file 至少要有 `LLM_API_BASE=http://your-gateway:8000/v1`。每次启动都会导入该文件并配置 Claude；凭据通过环境变量或挂载文件提供，不写进镜像。没有选择 Magpie 模型时，仍可使用 Claude 自身的登录配置。

```bash
./docker/team-worker.sh attach   # 回到 Leader；Ctrl-b w 选择其他窗口，Ctrl-b d 脱离
./docker/team-worker.sh shell    # 进入容器，可使用 team-worker / claude / git
./docker/team-worker.sh web      # 显示看板与 Magpie 的完整访问链接
./docker/team-worker.sh magpie   # Magpie 终端界面
./docker/team-worker.sh stop     # 停止所有 Agent 并删除容器，保留项目和 home 卷
```

## 启动流程与任务通知

容器启动时依次：导入 Magpie 配置（providers、插件、技能、MCP、Instructions）→ 启动 `team-workerd` → 启动只读看板和 Nginx → 注册团队 `team-worker-docker`（`TEAM_WORKER_TEAM_NAME` 可改）和 Leader → 在 tmux 中启动 Leader → 启动 `team-worker-ops leader watch`。已有团队会复用；Worker 在 Leader 分配任务时才创建。

消息由 team-worker 自己送到 Agent 面前，规则和容器外完全一样（见仓库的 [docs/agents.md](../docs/agents.md)）：

- Leader 和 Claude Worker 装有 team-worker hook：每轮结束时收到新消息、自己的待办任务和团队进展；干活途中也会收到新消息。Leader 的 hook 由入口脚本通过 `claude --settings "$(team-worker hook settings --role leader)"` 装上。
- pi Worker 由内置扩展在空闲时自己开始新一轮。
- Agent 停在输入框、hook 不会触发时，team-workerd 往它的 tmux 窗口输入一行提醒；权限确认和选择菜单不会被输入。
- `leader watch` 每 2 秒检查一次：出现新的、没有负责人的 pending 任务时，给 Leader 的收件箱发一条消息。它不读取也不移走任何收件箱。

与 ClawTeam 面板不同，team-worker 的看板是只读的，没有 New Task 按钮，也没有 OpenAPI。新增任务在容器内用 CLI：

```bash
./docker/team-worker.sh shell
team-worker task create team-worker-docker "实现登录功能" -d "说明……"
```

Leader 的 Claude 进程退出后，10 秒后重启，用 `TEAM_WORKER_LEADER_RESTART_DELAY` 调整间隔。供应商凭据错误需自行修正；tmux 会保留错误信息，不能只凭会话存在就判断模型可用。

## 更新二进制而不重建镜像

镜像里编译了一份 `team-worker`、`team-workerd` 和 `team-worker-ops` 放在 `/usr/local/bin`，但 `PATH` 优先使用 `/opt/team-worker/bin`。`start` 发现宿主机的二进制目录（默认仓库的 `bin/docker`，`TEAM_WORKER_BIN_DIR` 可改）里三个文件齐全时，会把它只读挂载到这里。所以改完代码只需要：

```bash
./docker/team-worker.sh bins      # 在宿主机编译静态二进制到 bin/docker
./docker/team-worker.sh restart   # 重启容器，team-workerd 和 Leader 用上新版本
```

`bins` 先用 `make web` 构建看板页面（需要宿主机有 Node.js 和 npm），再用宿主机的 Go 编译（`CGO_ENABLED=0`），不依赖容器里的 glibc。只换 `team-worker` CLI 时不重启也立即生效；`team-workerd` 和 `team-worker-ops leader watch` 是长期运行的进程，要重启容器才会换成新版本。重启会结束正在运行的 Agent。

## 一个端口访问多个面板

Nginx 在同一地址、同一端口下按路径分发：

| 路径 | 界面 | 容器内部地址 |
| --- | --- | --- |
| `/team-worker/` | team-worker 看板（只读，可看 Agent 实时终端） | `127.0.0.1:8081` |
| `/magpie/` | Magpie 管理界面 | `127.0.0.1:3430` |
| `/ui/`、`/v1/` | OpenBao 界面与 API（启用时） | `127.0.0.1:8200` |

根路径跳转到 `/team-worker/`。看板标题栏右侧有 Magpie 链接，启用 OpenBao 时还有 OpenBao 链接（入口脚本通过 `board serve --link` 传入）。Magpie 链接不带登录密钥：浏览器先用 `web` 打印的完整链接登录一次，之后从看板点 Magpie 就能直接进入。用 `./docker/team-worker.sh web` 获取完整地址和带登录密钥的 Magpie 链接；密钥保存在 home 卷，重启不变。Nginx 关闭了访问日志，避免记录链接密钥。看板和 Magpie 页面使用根路径 API，由 `subpath.js` 给页面请求加上各自前缀。

默认绑定本机 `127.0.0.1:8080`。用服务器 IP 访问时指定监听地址和对外 URL：

```bash
TEAM_WORKER_WEB_BIND=0.0.0.0 TEAM_WORKER_WEB_PORT=18080 \
  TEAM_WORKER_WEB_URL=http://192.168.1.10:18080 \
  ./docker/team-worker.sh start /项目目录
```

`TEAM_WORKER_WEB_URL` 不带末尾斜线。只发布 Nginx 端口，Magpie 的 `3425` 模型网关仅供容器内 Agent 使用。

## 日志

team-workerd 在容器里以前台方式运行，日志写到 stderr，和其他服务的输出一起用 `docker logs <容器名>` 查看。想单独保存到 home 卷，在 env-file 里设置 `TEAM_WORKER_LOG_FILE=/home/node/.team-worker/team-workerd.log`（按大小轮转）。级别和格式用 `TEAM_WORKER_LOG_LEVEL`、`TEAM_WORKER_LOG_FORMAT` 设置，例如排查问题时设 `TEAM_WORKER_LOG_LEVEL=debug`，接入日志系统时设 `TEAM_WORKER_LOG_FORMAT=json`。记录的内容见仓库根目录 README 的"日志"一节。

## 容器环境变量

| 变量 | 用途 |
| --- | --- |
| `LLM_API_BASE` | 默认 `providers.yaml` 的 OpenAI 兼容网关地址 |
| `TEAM_WORKER_TEAM_NAME` | 容器团队名，默认 `team-worker-docker` |
| `TEAM_WORKER_MODEL` | Magpie 模型路由，优先于 `providers.yaml` 的 `claude.model` |
| `TEAM_WORKER_LEADER_RESTART_DELAY` | Leader 退出后的重启间隔（秒），默认 10 |
| `TEAM_WORKER_DIND` | 设为 `1` 启用 Docker in Docker（systemd 模式） |
| `TEAM_WORKER_MASKING` | 默认在 home 卷首次启动时打开 Magpie 的 Privacy masking（发给模型供应商的密钥、密码、token 换成占位符）；之后在 Magpie 里关掉会保持关闭。设为 `0` 跳过 |
| `TEAM_WORKER_OPENBAO`、`TEAM_WORKER_OPENBAO_AUTO_UNSEAL` | 设为 `1` 启用 OpenBao 及自动初始化、解封 |
| `TEAM_WORKER_*_FILE` | `PROVIDERS`、`SKILLS`、`PROMPT`、`MCP`、`PLUGIN`、`OPENBAO` 配置文件的容器内路径 |
| `MIRROR_HTTP_PROXY`、`HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY` | 代理；`NO_PROXY` 建议包含 `localhost,127.0.0.1,::1` |

`TEAM_WORKER_DATA_DIR` 固定为 home 卷里的 `/home/node/.team-worker`，团队、任务和消息的 SQLite 库在这里，容器重建后保留。

env-file 也可以放部署者自己的变量（Docker env-file 的 `KEY=value` 格式），会进入 Leader、Worker 和 `docker exec` 打开的 shell。成员使用的密钥建议放进 OpenBao，不要写进 env-file。修改 env-file 后要重建容器；`docker start` / `docker restart` 沿用创建时的环境变量，`providers.yaml` 则每次启动重新读取。

## Docker in Docker

镜像包含 Docker CLI、daemon、Buildx 和 Compose。需要让 Agent 构建镜像或运行容器时：

```bash
TEAM_WORKER_DIND=1 TEAM_WORKER_ENV_FILE=/path/to/team-worker.env \
  ./docker/team-worker.sh start /项目目录
```

启动脚本会加上 `--user root --privileged --init=false`，把独立的 `<容器名>-docker` 数据卷挂到 `/var/lib/docker`。DinD 模式下 systemd 作为 PID 1：`docker.service` 以 root 守护 dockerd，`magpie.service` 管理 Magpie，`team-worker.service` 以 `node` 启动 team-workerd、看板、Leader 和 Worker。

```bash
docker exec --user root team-worker systemctl status docker
docker exec --user root team-worker systemctl status team-worker
docker exec --user root team-worker journalctl -u team-worker --no-pager -n 100
```

代理只写入 dockerd 的 systemd 环境和 `~/.docker/config.json` 的 `proxies.default`，让内层 `docker build` / `docker run` 获得代理；Agent 和 Magpie 的原始代理变量保持不变。

## Magpie 配置目录

`TEAM_WORKER_CONFIG_DIR` 指定一个配置目录（默认 `docker/magpie-config`），`start` 从这里读取 `providers.yaml`、`skills.yaml`、`prompt.yaml`、`mcp.yaml`、`plugin.yaml`；目录里如果还有 `openbao.yaml`、`openbao.token`、`gitconfig`，分别挂载到 `/etc/team-worker/openbao.yaml`、`/etc/team-worker/openbao.token`、`/etc/gitconfig`。单独的 `TEAM_WORKER_*_FILE` 仍然优先。

带凭据的真实部署配置（例如从服务器拷来的一份）放在 `docker/magpie-config.local/`：这个目录和所有 `*.env` 文件都在 `.gitignore` 里，不会被提交。

```bash
TEAM_WORKER_CONFIG_DIR=docker/magpie-config.local \
  TEAM_WORKER_ENV_FILE=docker/magpie-config.local/team-worker.env \
  ./docker/team-worker.sh start /项目目录
```

启用 OpenBao 且自动解封时，入口脚本会先等 OpenBao Agent 把 `openbao.yaml` 声明的文件都渲染出来（最多 180 秒），再导入技能；这样 `gitconfig` 里的凭据助手在技能克隆私有仓库时已经能读到凭据。

`magpie-config/` 存放 `providers.yaml`、`skills.yaml`、`prompt.yaml`、`mcp.yaml`、`plugin.yaml` 和 `openbao.yaml`，格式与 ClawTeam-Docker 相同，每个文件里有注释示例。启动脚本默认挂载本目录下的文件，也可用 `TEAM_WORKER_PROVIDERS_FILE`、`TEAM_WORKER_SKILLS_FILE`、`TEAM_WORKER_PROMPT_FILE`、`TEAM_WORKER_MCP_FILE`、`TEAM_WORKER_PLUGIN_FILE` 指定宿主机文件。使用 Compose 时把整个目录只读挂载到 `/etc/team-worker`：

```yaml
services:
  team-worker:
    image: team-worker-local:latest
    init: true
    restart: unless-stopped
    env_file: team-worker.env
    ports:
      - 127.0.0.1:8080:8080
    volumes:
      - /你的Git项目:/workspace
      - ./docker/magpie-config:/etc/team-worker:ro
      - team-worker-home:/home/node
volumes:
  team-worker-home:
```

- **providers.yaml**：按 ID 创建或更新 provider 和路由组，`claude.model` 选默认模型；`key` / `key_env`、`url` / `url_env` 各选一个，需要凭据的供应商设 `key_required: true`，Key 为空时 Leader 等待配置。`stays: "off"` 要加引号。
- **skills.yaml**：启动时通过 Magpie Library 安装来源中的新技能并同步；已有同名技能保留。DinD 模式下 `team-worker-skills-update.timer` 每小时更新 Git 来源的技能；非 systemd 模式可手动运行 `docker exec --user node team-worker team-worker-ops skills --update`。
- **prompt.yaml**：导入 Magpie Instructions，只更新文件中写明的字段。
- **mcp.yaml**：按名称创建或更新 MCP 服务，`install` 可声明安装和检查命令（参数列表，不经 Shell）。
- **plugin.yaml**：预装 Magpie 插件；单个插件安装失败只记警告，下次启动重试。

任一配置格式错误、技能或 MCP 导入失败时，启动中止，Leader 不运行，用 `docker logs` 查看原因。镜像自带的 team-worker skill 链接到 `~/.claude/skills/team-worker` 和 `~/.codex/skills/team-worker`。

## OpenBao 密钥服务（可选）

设置 `TEAM_WORKER_OPENBAO=1` 后启动 OpenBao 服务端（只监听容器内 `127.0.0.1:8200`，数据在 home 卷 `~/.openbao/data`），界面见 [OpenBao 界面使用指南](docs/openbao.md)。首次启用后应立即初始化。`magpie-config/openbao.yaml` 声明要导入的 KV 密钥和要用 OpenBao Agent 渲染的 0600 文件；导入令牌放在 `/etc/team-worker/openbao.token`（`TEAM_WORKER_OPENBAO_TOKEN_FILE` 可改）。授予 Agent 的只读 AppRole 名为 `team-worker-agent`。

同时设置 `TEAM_WORKER_OPENBAO_AUTO_UNSEAL=1` 时全自动初始化和解封，解封密钥与 root 令牌写入 home 卷的 `~/.openbao/init.json`。这样取得卷即可解开全部密钥，需要限制卷和宿主机的访问。

## team-worker-ops

入口脚本和 systemd 单元通过它完成各项配置，也可以在容器里手动运行：

| 子命令 | 作用 |
| --- | --- |
| `proxy` | 把代理写入 `~/.docker/config.json`，保留 registry 登录 |
| `magpie FILE` | 导入 providers.yaml，标准输出打印选定的模型 |
| `plugins FILE` / `skills [--update] [FILE]` / `mcp FILE` / `prompt FILE` | 导入插件、技能、MCP、Instructions |
| `openbao FILE` | 导入密钥并启动 OpenBao Agent |
| `systemd prepare\|agent\|magpie` | DinD 模式下把运行配置交给 systemd，并启动对应服务 |
| `leader prepare\|register\|watch` | 注册团队与 Leader（开启终端提醒），把新的未分配任务发到 Leader 收件箱 |
| `openbao --wait-rendered DURATION FILE` | 等待 OpenBao Agent 渲染 openbao.yaml 声明的文件 |

开发与测试：

```bash
cd docker/ops && go test ./...
```

## 构建参数

```bash
GOPROXY=https://goproxy.cn,direct CLAUDE_VERSION=<npm版本> CODEX_VERSION=<npm版本> PI_VERSION=<npm版本> \
  BUN_VERSION=<版本> DSH_VERSION=<npm版本> DSH_TUI_VERSION=<npm版本> \
  PLAYWRIGHT_VERSION=<pip版本> OPENBAO_VERSION=<版本> MAGPIE_VERSION=<镜像tag> \
  ./docker/team-worker.sh build
```

国内网络可以换用镜像源，例如：

```bash
GOPROXY=https://goproxy.cn,direct DEBIAN_MIRROR=mirrors.aliyun.com \
  NPM_REGISTRY=https://registry.npmmirror.com PIP_INDEX_URL=https://pypi.tuna.tsinghua.edu.cn/simple \
  BUN_DOWNLOAD_BASE=https://registry.npmmirror.com/-/binary/bun \
  PLAYWRIGHT_DOWNLOAD_HOST=https://cdn.npmmirror.com/binaries/playwright \
  ./docker/team-worker.sh build
```

不设 `PLAYWRIGHT_DOWNLOAD_HOST` 时，Playwright 的 Chromium 从官方地址下载。其他下载慢时可以给构建加代理，并用 `NO_PROXY` 让国内镜像直连；代理在本机回环地址时脚本会自动加 `--network host`：

```bash
HTTP_PROXY=http://127.0.0.1:7897 HTTPS_PROXY=http://127.0.0.1:7897 \
  NO_PROXY=localhost,127.0.0.1,.npmmirror.com,.aliyun.com,goproxy.cn,.tuna.tsinghua.edu.cn \
  ./docker/team-worker.sh build
```

构建上下文是仓库根目录（`docker build -f docker/Dockerfile .`），镜像里的 team-worker 和 team-worker-ops 都从当前工作区源码编译。默认镜像名 `team-worker-local:latest`，容器名 `team-worker`，用 `TEAM_WORKER_IMAGE`、`TEAM_WORKER_CONTAINER` 覆盖。镜像设置 `DISABLE_AUTOUPDATER=1`，工具版本随镜像固定；同时预装 Bun（Magpie 插件运行时）、Python Playwright 与 Chromium（浏览器测试）、dsh 与 dsh-tui。

项目挂载到 `/workspace`，修改直接写回宿主机；Linux 上项目需允许容器用户 UID 1000 读写。`<容器名>-home` 卷保存 Claude 登录状态、team-worker 数据库和 Magpie 配置；重启容器可保留这些文件，但不会恢复已结束的 Agent 进程或 tmux 会话。

## 与 ClawTeam-Docker 的差异

- 团队、任务、消息由 team-workerd 存在 SQLite 中，不再是 `~/.clawteam` 下的文件。
- 看板只读，没有 New Task 与 `/clawteam/openapi/`，`CLAWTEAM_BOARD_API_KEY` 不再使用。
- 环境变量前缀从 `CLAWTEAM_` 改为 `TEAM_WORKER_`，配置挂载点从 `/etc/clawteam` 改为 `/etc/team-worker`，面板路径从 `/clawteam/` 改为 `/team-worker/`。
- 启动配置脚本由 Python 改为 Go 程序 `team-worker-ops`，镜像不再需要 PyYAML。
- 未移植：Vikunja 桥接、容器级集成测试（`smoke.py`、`dind_smoke.py`）和 GitHub 镜像发布工作流。

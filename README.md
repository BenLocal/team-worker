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

### 加入审批

`team add-member` 直接加人。想先让 leader 同意再加入时，用加入申请：

```bash
# 申请者：默认等待 leader 答复（-t 秒数，默认 300）；--no-wait 只登记申请
team-worker team request-join my-team alice -c "熟悉 Go 和测试"
team-worker team join-status my-team <申请ID>

# Leader：查看、批准（可用 --assigned-name 改名）或拒绝
team-worker team join-requests my-team --status pending
team-worker team approve-join my-team <申请ID>
team-worker team reject-join my-team <申请ID> -r "人数已满"
```

申请记录保存在数据库里，状态为 pending、approved 或 rejected。申请时 leader 收件箱会收到 `join_request` 消息。只有 leader 能批准或拒绝，而且只能处理仍在 pending 的申请；同一个名字已有待处理申请或已是成员时，再次申请会报冲突。批准时成员加入和 `join_approved` 通知在同一个事务里完成，新成员的收件箱会收到分配的名字和 Agent ID，申请者需要把它们设为 `TEAM_WORKER_AGENT_NAME`、`TEAM_WORKER_AGENT_ID`。被拒绝的申请者还不是成员，没有收件箱，`request-join` 等待中或 `join-status` 直接读取保存的拒绝原因；`request-join` 等到拒绝时退出码为 1。

### 计划审批

成员可以先把计划交给 leader 审批再动手：

```bash
# 成员：PLAN 可以是文本，也可以是文件路径；-s 是一句话摘要
team-worker plan submit my-team alice plan.md -s "先写接口再补测试"

# Leader：查看、批准或拒绝（-f 附反馈）
team-worker plan list my-team --status pending
team-worker plan show my-team <计划ID>
team-worker plan approve my-team <计划ID> alice -f "可以开始"
team-worker plan reject my-team <计划ID> alice -f "先补上回滚步骤"
```

计划正文和审批结果保存在数据库里，状态为 pending、approved 或 rejected。提交时 leader 收件箱收到 `plan_approval_request`，审批后成员收到 `plan_approved` 或 `plan_rejected`（带反馈）。只有 leader 能审批，而且只能处理仍在 pending 的计划。被拒后修改再提交会得到新的计划 ID，旧计划保留原状态。

需要强制执行时，用 `team add-member --plan-required` 或 `spawn --plan-required` 打开成员的审批要求。打开后，该成员在有计划被批准之前，把任务改为 `in_progress` 或 `completed` 会被服务端拒绝；查看任务、收发消息、提交计划不受影响。`spawn --plan-required` 还会设置 `TEAM_WORKER_PLAN_MODE_REQUIRED=1`，并在首条提示词里说明审批流程，`identity show` 显示为 `planModeRequired`。没有打开要求的成员不受影响。

### 用量与预算

```bash
# 上报一次用量和费用（--agent 默认取当前身份）
team-worker cost report my-team --input-tokens 12000 --output-tokens 800 --cost-cents 9.5 --model sonnet

# 团队汇总、各成员用量和明细（-a 只筛选明细）
team-worker cost show my-team

# 预算 20 美元；--enforce 表示用完后阻止新工作，0 表示不限
team-worker cost budget my-team 20 --enforce

# 立即从成员的原生会话文件读取 token 用量
team-worker cost sync my-team
```

用量有两个来源。`cost report` 是手动上报，可以带费用；Agent 的首条提示词会提醒它把知道的费用报上来。另外，Agent 退出时会自动读取它的原生会话文件记录 token 用量，`cost sync` 可以随时手动触发：Claude Code 读 `~/.claude/projects/*/<会话ID>.jsonl`（缓存读写计入输入 token，同一条消息只计一次），Codex 读 `~/.codex/sessions/` 下的 rollout 文件（取最后一次累计值）。自动记录只有 token、没有费用，因为不维护价格表；同一会话再次读取时覆盖之前的记录。其他 CLI 暂不支持自动读取。

花费第一次达到预算时，leader 收到一条来自 `team-worker` 的消息，之后不再重复，直到重新设置预算。默认只提醒、不阻止；加 `--enforce` 后，预算用完时把任务改为 `in_progress` 会被服务端拒绝，`spawn` 也不再启动新 Agent，已在进行的任务仍可完成。`cost show --json` 的格式与 ClawTeam 一致（`budget_cents` 保持下划线），另外增加 `budgetEnforce` 和按成员的 token 汇总 `byAgentTokens`。

### 阶段编排（harness）

```bash
# 开一轮：discuss → plan → execute → verify → ship；团队不存在时以当前身份为 leader 创建
team-worker harness start -g "给待办应用加上标签功能" -t my-team -n 3

# 前台运行，按阶段自动拉起规划者、执行者、评审者，关卡满足后自动推进；Ctrl+C 不影响这一轮
team-worker harness conduct my-team

# 手动操作
team-worker harness status my-team
team-worker harness approve my-team          # 批准规划者提交的计划（仅 leader）
team-worker harness advance my-team
team-worker harness abort my-team -r "目标有变"

# Agent 登记交付物、拆分任务时用
team-worker harness artifact my-team spec.md --file spec.md
team-worker harness artifacts my-team --show spec.md
team-worker harness task my-team "实现标签接口" -d "按 spec.md 第 2 节"
```

每轮的阶段、交付物、历史和 Agent 都保存在数据库里，一个团队同时只有一轮在运行。离开每个阶段前，服务端检查关卡：

| 阶段 | 角色 | 进入下一阶段的条件 |
|---|---|---|
| discuss | 规划者 | 已登记 `discussion.md` |
| plan | 规划者 | 已登记 `spec.md`，且规划者在本轮提交的计划已被 leader 批准（走「计划审批」流程，`harness approve` 只是快捷方式） |
| execute | 执行者（`-n` 个） | 本轮当前尝试至少有一个任务（`harness task` 创建），且全部完成 |
| verify | 评审者 | `eval-report.json` 为 JSON，`verdict` 为 `pass` |
| ship | — | 进入即完成 |

评审结论为 `fail` 时，推进会把这一轮退回 execute：尝试次数加一，历史记为 `rolled_back`，自动建一个带评审反馈的返工任务，并把反馈发给本轮的执行者。尝试次数达到 `--max-attempts`（默认 2）后再失败，这一轮记为 failed。

`harness conduct` 每次轮询都从数据库重新读取状态，所以中途可以在别处手动 approve、advance 或 abort。它为当前阶段补齐需要的角色 Agent（名字如 `executor-3fa9c1`，用 `spawn tmux --keepalive` 启动，并记录在这一轮里，重新运行 conduct 不会重复启动），停掉已结束阶段的 Agent，在这一轮完成、失败或中止后退出。`harness abort` 会停掉这一轮仍在运行的 Agent。`harness status --json` 的字段沿用 ClawTeam 的下划线写法（`harness_id`、`can_advance`、`gate_reason` 等）。

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

Claude Code 的 Agent 还会自动装上 team-worker 的 hook：它准备停下时，会先收到新消息、自己的待办任务，以及（对 leader 而言）团队的进展；干活途中也能收到消息；上下文被压缩后，会重新拿到团队信息。Agent 已经停在输入框时，hook 不会被触发；这时如果有未读消息，daemon 会往它的 tmux 终端里输入一行提醒，把它唤醒。是否停在输入框由 hook 上报的状态决定（工作中、等待中、等用户确认），没有 hook 的 CLI 才看终端画面；`agent list` 的 ACTIVITY 列和看板也显示这个状态。这一机制对所有 tmux 后端的 CLI 都有效，也包括没有 hook 的 CLI。你在自己终端里启动的 Claude 会话也能用这套 hook：`claude --settings "$(team-worker hook settings --role leader)"`。详见 [docs/agents.md](docs/agents.md#hook让消息和任务自动送到-agent-面前)。pi 通过内置扩展接入同一套逻辑。和 Claude 不同，pi 停在输入框时，扩展会自己开启新的一轮，不需要往终端里打字提醒。Codex 的 hook 目前是实验性功能，默认关闭。

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

## 运行配置与端点预设

**运行配置**（profile）把启动一个 Agent 要用的命令、模型、模型端点和凭据变量记在一个名字下；**端点预设**（preset）描述一个模型服务的地址和鉴权变量，再按 CLI 生成运行配置。两者都保存在 `~/.team-worker/config.json`，字段名与 ClawTeam 相同，原有配置可以直接沿用。

```bash
team-worker preset list                                  # 内置 13 个预设，本地同名预设覆盖内置的
team-worker preset generate-profile deepseek claude     # 生成运行配置 claude-deepseek
team-worker profile set kimi-cn --command "kimi --config-file ~/.kimi/config.toml" --model kimi-k2
team-worker profile test claude-deepseek                 # 用一句提示实际跑一次，报告命令、退出码和输出
team-worker spawn tmux --team my-team -n alice --profile claude-deepseek --task "..."
team-worker launch my-template -g "..." --profile claude-deepseek
team-worker agent resume my-team alice --profile claude-deepseek   # 续接时重新带上运行配置的变量
```

`spawn` 选用运行配置的顺序：`--profile`；命令写在 `--` 后面时不用任何运行配置；`default_profile`（环境变量 `TEAM_WORKER_DEFAULT_PROFILE` 优先）；只配置了一个时用它；配置了多个却没指定时报错。

运行配置的环境变量位于适配器之后、模板和 `--env` 之前，所以模板和 `--env` 可以覆盖它；`--dry-run` 把这一层显示为 `profile <名字>`。其中 `env` 和 `env_map` 直接设置变量；`base_url` 和 `api_key_env` 只在变量还没设置时，按 CLI 填入对应的变量（claude 为 `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN`，codex 为 `OPENAI_BASE_URL` / `OPENAI_API_KEY`，gemini、kimi 类似），也可以用 `base_url_env`、`api_key_target_env` 指定。命令里没有 `--model` 或 `-m` 时，claude、codex、gemini、kimi、pi 会加上运行配置的模型。

凭据只记环境变量名：`api_key_env` 和 `env_map` 的来源变量在启动时从调用 `spawn` 的环境读取，配置文件里不保存密钥本身。来源变量没有设置或为空时，对应的变量不会注入。

命令一览：`preset list|show|set|set-client|copy|remove|remove-client|generate-profile|bootstrap`，`profile list|show|set|remove|test`。`set` 只修改给出的参数，`--env`、`--env-map`、`--arg` 会整体替换原有值。ClawTeam 的交互式 `profile wizard` 和 `profile doctor` 没有实现。

## 会话恢复与 keepalive

`spawn` 会保存原生 CLI 的会话信息，进程退出不删除这些信息。Claude 和 pi 在启动前分配明确的会话 ID；Codex 优先从启用的会话钩子获取 ID，未启用钩子时，在退出、查看或恢复时按工作目录和 Agent 身份匹配本地记录。匹配不唯一时会报错，不自动选择最近会话。其他 CLI 可用 `session save` 提供准确 ID。

```bash
team-worker session show my-team --agent alice
team-worker session save my-team --agent alice --session-id <原生会话ID>
team-worker agent resume my-team alice --dry-run
team-worker agent resume my-team alice

# keepalive 默认关闭；开启后在未经批准的退出后续接同一会话。
team-worker spawn tmux --team my-team -n alice --keepalive --max-restarts 3 --restart-delay 1s -- claude
```

恢复复用原 CLI 命令、工作目录和权限设置，并重新读取团队说明、成员提示词和当前任务。已有运行中的同名 Agent 时拒绝恢复，可用 `--replace` 明确替换。`agent resume` 支持 `--task`、`--keepalive`、`--max-restarts`、`--restart-delay` 覆盖保存的设置。

keepalive 适用于 tmux 和 subprocess，正常退出和异常退出均受重试上限约束；默认最多连续重启 3 次，等待时间逐次加倍、最长 1 分钟，一次运行达到 1 分钟后重置连续次数。原生会话 ID 缺失时停止恢复并报错。每轮进程使用独立 `run_id`，旧进程的退出回报不会修改新一轮的状态。退出时任务仍退回 pending，恢复后需重新检查任务归属并领取。

`agent stop`、批准 shutdown 和 `team cleanup` 会先保存停止意图，阻止后续重启。整个 tmux pane 或 supervisor 被结束后不会自动创建新 pane，可使用 `agent resume` 手动恢复。daemon 重启后已保存的会话仍可读取。

数据库只保存会话标识和启动信息，原生 CLI 的历史文件仍需保留。环境变量的值不随会话保存；通过模板或 `--env` 设置的变量，手动恢复时需在调用者环境或新的 `--env` 参数中提供。Claude、Codex、Gemini、Kimi、Qwen、OpenCode、pi 和 dsh 分别构造自己的精确会话恢复参数；具体可用性取决于安装的 CLI 版本，未知 CLI 不支持自动恢复。

恢复流程的可重复验收（使用隔离数据目录，不影响现有团队）：

```bash
make build
python3 scripts/verify-sessions.py --bin-dir bin
python3 scripts/verify-sessions.py --bin-dir bin --backend tmux
# 使用现有 Claude provider 配置，额外发起两次真实模型请求：
python3 scripts/verify-sessions.py --bin-dir bin --native-claude "$(command -v claude)"

# 其他原生 CLI 的真实会话验收，每次发起两次模型请求：
python3 scripts/verify-native-sessions.py --client codex --timeout 180 -- codex --skip-git-repo-check --json
python3 scripts/verify-native-sessions.py --client pi -- pi --no-tools --no-mcp --mode json --thinking off
python3 scripts/verify-native-sessions.py --client dsh -- dsh --profile headless --json
```

`verify-native-sessions.py` 检查准确会话 ID、新一轮 Run ID 和不调用工具的暗号回忆；日志保留在输出的临时目录。dsh 的会话 ID 从事件流读取后通过 `session save` 保存；如需复用网页保存的凭据或覆盖 headless 配置，在原生命令中追加本地 `--patch` 文件。

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

Web 看板默认只监听 127.0.0.1，访问需使用与 CLI HTTP 调用相同的 Key。默认 Key 自动生成并保存在 SQLite 中，重启复用；可在本机通过 `team-worker key show` 查看。页面登录后，`Service keys` 可查看 Magpie Web Key 和 OpenBao 登录 Token，默认遮挡并支持显示、复制。 `board serve --link NAME=URL`（可重复）在页面标题栏右侧显示到其他服务的链接，例如 `--link Docs=https://example.com`。

团队标题旁的 `Team prompt` 按钮显示团队公共提示词。每个 Agent 终端卡片的 `Prompt` 按钮和侧栏成员菜单的 `Show prompt` 显示该成员的专属提示词，未启动或已退出的成员也能查看。内容从 store 按需读取，未设置时显示空状态；弹窗支持长文本滚动及 Escape 关闭。

## Agent skill

[`skills/team-worker`](skills/team-worker/SKILL.md) 说明 leader 和 worker 怎样用这个 CLI 协作，以及 worker 的循环流程。安装时把整个目录复制或软链到 `~/.claude/skills/team-worker`（Claude Code）或 `~/.codex/skills/team-worker`（Codex）。

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

### HTTP 远程调用

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

Web 看板的提示词读取接口由 `board serve` 提供：

| 接口 | 说明 |
|---|---|
| `GET /api/service-keys` | 登录后按需读取 Magpie Web Key 和 OpenBao 登录 Token，响应禁止缓存 |
| `GET /api/teams/{team}/prompt` | 查看团队公共提示词 |
| `GET /api/teams/{team}/members/{name}/prompt?user=...` | 查看成员专属提示词，`user` 默认空 |

后续需求见 [TODO.md](TODO.md)。

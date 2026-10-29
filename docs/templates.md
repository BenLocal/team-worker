# 团队模板

`team-worker launch` 根据模板创建团队，用一条命令完成以下工作：登记 leader 和成员，按依赖关系创建任务，然后依次启动 leader 和所有 worker。team-worker 不自带模板，团队里有哪些角色、各自做什么、任务怎么拆，都由你在模板中定义。

## 放在哪里

模板是 TOML 文件，放在 `~/.team-worker/templates/<name>.toml`，按文件名引用：

```bash
team-worker template list            # 列出目录里的模板
team-worker template show my-team    # 查看成员和任务
team-worker launch my-team -g "目标"  # 创建团队并启动
```

也可以直接传入文件路径：`team-worker launch ./teams/review.toml -g "..."`，适合把模板和项目代码放在同一个仓库里管理。

## 格式

所有内容都写在 `[template]` 表下。

```toml
[template]
name = "my-team"              # 可省略，默认取文件名
description = "一句话说明"
command = ["claude"]          # 所有 Agent 默认使用的 CLI，默认 ["claude"]
backend = "tmux"              # tmux 或 subprocess，默认 tmux

[template.env]                # 可选，所有 Agent 共用的环境变量，值里可以用 ${NAME}
GIT_AUTHOR_NAME = "team-bot"

[template.leader]             # 必填，团队的 leader
name = "lead"
type = "lead"                 # 角色名，写入成员信息和身份变量，默认 general-purpose
task = """leader 的任务说明"""
command = ["codex"]           # 可选，单独为这个 Agent 指定 CLI
env = { OPENAI_API_KEY = "${LEAD_KEY}" }   # 可选，只给这个 Agent 的环境变量

[[template.agents]]           # 可写多个，每个对应一个 worker
name = "dev"
type = "developer"
task = """worker 的任务说明"""

[[template.tasks]]            # 可写多个，启动前创建到任务板上
key = "impl"                  # 可选，供其他任务的 blocked_by 引用
subject = "实现功能"
description = "可选的详细说明"
owner = "dev"                 # 负责人，填成员名
priority = "high"             # low、medium、high、urgent，默认 medium
blocked_by = ["design"]       # 依赖的任务，填其他任务的 key
```

`${NAME}` 取自执行 `launch` 时所在的环境，所以模板里不必写明文密钥。完整的环境变量合成规则，以及怎样给某种 CLI 统一配置环境变量，见 [agents.md](agents.md)。

`launch` 会校验 `blocked_by`：其中每个 key 都必须在模板里的某个任务上定义过，否则直接报错，不会创建任何东西。

### 变量

`task` 和任务的 `description` 中可以使用以下变量，`launch` 时会被替换：

| 变量 | 替换为 |
|---|---|
| `{goal}` | `launch -g` 传入的目标 |
| `{team_name}` | 团队名（`launch -t` 指定，默认 `<模板名>-<随机后缀>`） |
| `{agent_name}` | 当前 Agent 的名字 |
| `{leader_name}` | leader 的名字 |

不在上表里的 `{...}` 会原样保留。

## 每个 Agent 实际收到什么

`task` 不是 Agent 收到的完整提示词。`launch` 和 `spawn` 会在它前后补上固定内容，按顺序是：

1. 身份：名字、ID、角色、团队名、leader 名字。
2. 工作目录：默认是 `~/.team-worker/workspaces/<team>/<agent>`，每个 Agent 一个，互不共享。用 `launch --cwd` 可以让所有 Agent 改用同一个目录。
3. 团队说明：`launch --team-prompt-file` 写入的内容；团队运行中可以用 `team-worker team prompt set` 修改，修改后成员会收到通知。
4. 你写的 `task`。
5. 协作规则：怎样查任务、领取任务、汇报结果、等待新指令、响应关闭请求。

所以在 `task` 里只需要写这个角色的职责、工作方法和交付物，不必重复讲 team-worker 怎么用。仓库位置、测试环境这类整个团队通用的信息，放进团队说明里，就不用在每个 Agent 的 `task` 里重复写。

`launch` 将每个角色渲染后的 `task` 保存到 SQLite 的成员记录中。后续 `spawn` 不传 `--task` 会复用保存值，显式传入则覆盖，传空字符串则清空；`--dry-run` 不写入。运行中可以用 `team-worker agent prompt set <team> <agent> --file role.md` 单独修改，并通知该成员重新读取。会话恢复钩子也会恢复保存的专属提示词。修改源 TOML 文件不会更新已创建团队。

## 写好模板的几点建议

- **leader 要负责收尾。** 写清楚 leader 什么时候算完成，例如：用 `team-worker task wait {team_name}` 等所有任务完成，核对 worker 交付的结果，汇总后报告给用户，最后用 `team-worker lifecycle request-shutdown` 让 worker 退出。
- **任务的颗粒度以能独立交付为准。** 每个任务要有明确的负责人和完成标准，有先后顺序的用 `blocked_by` 连起来。依赖的任务完成后，被阻塞的任务会自动变回 `pending`。
- **写明交付方式。** 比如结果写到哪个文件、提交到哪个分支、用什么格式发给谁。例如 `team-worker inbox send {team_name} {leader_name} "FINDINGS: ..."`。
- **不同角色可以用不同的 CLI。** 在单个 Agent 下写 `command` 即可。`launch` 在 `--` 后面给出的命令，会覆盖所有 Agent 的 `command`。

## 示例

一个由 architect 带领的三人开发团队，可以以此为起点修改：

```toml
[template]
name = "software-dev"
description = "Architect-led team that designs, builds and tests a feature"

[template.leader]
name = "architect"
type = "architect"
task = """You lead team {team_name}. Goal: {goal}

1. Read the team instructions and the existing code, then write a short design: modules,
   interfaces, and which member owns which part.
2. Refine the tasks on the board to match the design; add missing ones with
   `team-worker task create {team_name} "<subject>" -o <member> --blocked-by <ids>`.
3. Send each member the design and their part with `team-worker inbox send`.
4. Mark your design task completed, then follow progress with
   `team-worker task wait {team_name} --timeout 1800` and answer questions as they arrive.
5. When every task is completed, check the results yourself, report to whoever started the
   team, and ask each member to stop with `team-worker lifecycle request-shutdown {team_name} <member>`."""

[[template.agents]]
name = "backend"
type = "backend-developer"
task = """You are the backend developer. Goal: {goal}
Wait for the architect's design in your inbox, then implement the server side and its tests.
Run the tests before marking a task completed."""

[[template.agents]]
name = "frontend"
type = "frontend-developer"
command = ["codex"]
task = """You are the frontend developer. Goal: {goal}
Wait for the architect's design, then build the UI against the agreed API. Settle API details
with backend through `team-worker inbox send {team_name} backend "..."`."""

[[template.agents]]
name = "tester"
type = "tester"
task = """You are the tester. Goal: {goal}
Your task unblocks when implementation is done. Run the feature end to end, add missing tests,
and report each defect to its owner and to {leader_name} with steps to reproduce."""

[[template.tasks]]
key = "design"
subject = "Design the solution and split the work"
owner = "architect"
priority = "high"

[[template.tasks]]
key = "backend"
subject = "Implement the backend"
owner = "backend"
blocked_by = ["design"]

[[template.tasks]]
key = "frontend"
subject = "Implement the frontend"
owner = "frontend"
blocked_by = ["design"]

[[template.tasks]]
subject = "Test the feature end to end"
owner = "tester"
blocked_by = ["backend", "frontend"]
```

启动方式：

```bash
echo "代码仓库在 /src/app，测试命令是 make test" > team-prompt.md          # 可选：团队说明
team-worker launch software-dev -t dev-team -g "给待办应用加上标签功能" --team-prompt-file team-prompt.md
tmux attach -t team-worker-dev-team    # 观察各 Agent 的终端
team-worker board serve                # 或在浏览器里打开看板
```

## 调试模板

- 改完模板，先用 `team-worker template show <name>` 检查成员和依赖关系对不对。
- 不想真的调用模型时，在 `--` 后面换成一个简单脚本，先把流程跑通：`team-worker launch <name> -t try-1 -- ./fake-agent.sh`。脚本可以读取 `TEAM_WORKER_AGENT_NAME` 等环境变量，再调用 `team-worker task update` 之类的命令。
- 试完用 `team-worker team cleanup <team> --force` 删除团队数据。还在运行的 Agent 用 `team-worker agent stop` 停止，或者直接关掉 tmux 会话。

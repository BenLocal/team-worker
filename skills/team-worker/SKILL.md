---
name: team-worker
description: 用 team-worker CLI 组织多 Agent 团队：建团队、拆分并分派任务、启动 worker Agent、收发 inbox 消息、等待全部任务完成。用户提到 team-worker 或要把一项工作拆给多个 Agent 并行做时触发；作为团队成员被启动、需要领取任务或汇报进度时也触发。
---

# 使用 team-worker 协调多 Agent

团队、任务、消息和 Agent 进程记录都保存在 `team-workerd` 服务的 SQLite 库里，所有操作都在终端里用 `team-worker` 命令完成。服务连不上时，CLI 会自动拉起 `team-workerd`。

## 准备

1. 运行 `team-worker --help`，确认 CLI 可用。如果找不到命令，到 team-worker 源码仓库执行 `make build`，再用 `bin/team-worker`。`team-workerd` 必须和它在同一个目录，或者在 `PATH` 里。
2. 运行 `team-worker identity show` 核对身份。发件人、任务锁的持有者和默认收件箱，都取自 `TEAM_WORKER_AGENT_NAME`。这个变量没设置时值是 `agent`，几个 Agent 会共用同一个身份。所以执行任何写操作前，先把它设成你在团队里的名字。由 `spawn` 启动的 Agent 已经设好了。

具体参数以 `team-worker <命令> --help` 为准。需要解析输出时加 `--json`。

## 作为 Leader

1. **建团队**：`team-worker team spawn-team <team> -d "<目标>"`，当前身份注册为 leader，默认名字是 `leader`，可以用 `-n` 改。如果团队里所有 Agent 要共用一些说明（仓库位置、测试方法等），用 `team-worker team prompt set <team> "<说明>"` 写入，之后启动的 Agent 在第一条提示词里就能看到。
2. **拆任务**：每个任务对应一个能独立交付的结果，用 `-o` 指定负责人。有先后顺序的任务用 `--blocked-by <id,id>` 声明依赖：被依赖的任务完成前，后面的任务保持 `blocked`；完成后自动变回 `pending`。
   ```bash
   team-worker --json task create <team> "设计 API" -o alice -p high
   team-worker --json task create <team> "实现后端" -o bob --blocked-by <设计任务ID>
   ```
   完成标准：`team-worker task list <team>` 里每个任务都有负责人，依赖关系和执行顺序一致。
3. **启动 worker**：每个负责人启动一次。
   ```bash
   team-worker spawn tmux --team <team> -n alice --agent-type developer --task "<职责和交付要求>"
   team-worker spawn tmux --team <team> -n bob --task "..." -- codex      # -- 后面写要用的 Agent CLI，默认 claude
   ```
   `spawn` 会做这些事：登记成员；给 Agent 建工作目录 `~/.team-worker/workspaces/<team>/<name>`（可以用 `--cwd` 换成别的目录）；注入身份变量；把身份、团队说明、任务和协作规则作为第一条提示词发给它。如果同名 Agent 还在运行，会报错；要先停掉旧的再重新启动，加 `--replace`。不确定会执行什么时，先加 `--dry-run` 查看最终命令和环境变量的来源。
   团队结构固定、要反复使用时，可以写一个模板，用 `team-worker launch <模板> -g "<目标>"` 一次建好团队、任务和所有 Agent。模板格式见 team-worker 仓库的 `docs/templates.md`。
4. **下达指令**：`team-worker inbox send <team> <name> "<说明>"`；要通知所有成员，用 `team-worker inbox broadcast <team> "<内容>"`。
5. **等待和跟进**：如果你是由 spawn 启动的 Claude Code，或者是用 `claude --settings "$(team-worker hook settings --role leader)"` 启动的，每次准备停下时都会自动收到新消息和团队进展，这时直接结束这一轮回复即可，不需要执行 `task wait`。没有 hook 时，执行 `team-worker --json task wait <team> --timeout <秒>`。等待期间它会读取 leader 收件箱里的消息，最后一行的 `"event": "result"` 中，`status` 为 `completed` 才代表全部完成；`timeout` 或 `interrupted` 时退出码为 1。如果 Agent 进程退出时手上还有任务，这些任务会自动退回 `pending`，leader 会收到一条 `exited unexpectedly` 消息。这时用 `spawn --replace` 重新启动这个 Agent，或者把任务改派给别人。要看某个 Agent 当前在做什么，用 `team-worker agent screen <team> <name>`；要看进程是否还活着，用 `team-worker agent list <team>`。
6. **验收和收尾**：逐个核对 worker 汇报的结果，确认无误后再向用户报告。用 `team-worker lifecycle request-shutdown <team> <name>` 让 worker 退出。如果 worker 不响应，用 `team-worker agent stop <team> <name>` 强制停止。用户确认团队不再需要后，执行 `team-worker team cleanup <team> --force`，这会删除该团队的任务和消息，但工作目录会保留。

## 作为 Worker

先用 `team-worker identity show` 核对身份，然后在第一条提示词给出的工作目录里，重复下面的循环，直到 leader 明确让你停止：

1. 用 `team-worker --json task list <team> --owner <你的名字>` 查看分给你的任务。列表为空时，再看 `team-worker --json task list <team> --status pending` 里有没有和你职责相符的任务，同时检查收件箱。
2. 开始一个任务：`team-worker task update <team> <id> -s in_progress`。这会以你的名字给任务加锁。
3. 完成工作。如果改动的文件在 git 仓库里，先提交，再进行下一步。
4. 标记完成：`team-worker task update <team> <id> -s completed`。
5. 向 leader 汇报结果：`team-worker inbox send <team> <leader> "<任务ID> 已完成：<结果和交付位置>"`。遇到阻塞也用这条命令说明情况。
6. 等待新指令：`team-worker --json inbox receive <team> --wait 60`。没有任务也没有消息时，执行 `team-worker lifecycle idle <team>`，然后回到第 1 步。
7. 收到 `shutdown_request` 类型的消息时，先完成手头的任务，或者把它改回 `pending` 交还出去，然后执行 `team-worker lifecycle approve-shutdown <team> <requestId>`，再退出。

## 规则

- `inbox receive` 会消费消息，读过的消息会从收件箱里删除；只想看看时用 `inbox peek`。历史消息用 `inbox log` 查看。
- 把任务改为 `in_progress` 时，如果报 `is locked by '<name>'`，说明别的成员正在做这个任务，此时去做其他任务，或者去问 leader。持锁的 Agent 进程退出后，锁会自动失效。只有在 leader 明确要求时，才加 `--force` 接管。
- 发送很长的内容时，省略消息参数，改从 stdin 传入：`team-worker inbox send <team> <to> < report.md`。
- 团队名和成员名只能用字母、数字、`.`、`_` 和 `-`。

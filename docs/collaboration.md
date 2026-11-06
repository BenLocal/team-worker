# 团队协作流程

> 加入审批、计划审批、请示（决策请求）与 AI 自动审核、团队说明与成员提示词、用量与预算、阶段编排（harness）、团队快照的命令和规则。

## 加入审批

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

## 计划审批

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

## 请示（决策请求）

Leader 的协作说明（2026-11-06 起）要求它自己拍板：团队说明授权范围内、可以回退的事（做法、先后顺序、几个方案里选哪个）由 leader 直接决定并执行，再把决定、比较过的选项和理由作为普通消息发给 `operator`，不停下来等同意。只有不可逆或破坏性、影响真实数据（删除、移动或清空数据、重刷、改凭据）、超出团队说明授权、影响团队以外的人或系统、涉及费用、团队说明要求向用户确认的事，或者目标本身不清楚时，leader 才发请示，并且带 `--human`，只能由人决定。

请示带选项和推荐项，在看板上可以直接决定：

```bash
# Leader：-d 写背景和风险（长内容用 --body-file，- 表示 stdin）；--option 按顺序给选项；--human 表示必须由人决定
team-worker request create my-team "缓存目录怎么处理？" -d "三种做法……" \
  --option "A=保持现状" --option "B=迁到新卷" --option "C=冷热分离" --recommend C
team-worker request create my-team "重刷测试板？" --human -d "会丢掉板上的日志"

# 操作员：查看和决定（--choice 批准某个选项，-m 附说明；没有选项时用 --approve）
team-worker request list my-team --status pending
team-worker request show my-team <请示ID>
team-worker request decide my-team <请示ID> --choice C -m "先在测试环境做"
team-worker request decide my-team <请示ID> --reject -m "等发布之后再说"
team-worker request decide my-team <请示ID> --reply "用 B，下周再拆"
```

- 请示保存在数据库里，状态为 pending、approved（批准，带所选选项）、rejected（拒绝，必须写原因）或 answered（直接用文字答复）。创建时收件箱 `operator` 收到 `decision_request` 消息（`inbox receive --agent operator`、`--remote` 下的 `task wait` 都能读到）；决定后请示人收到 `decision_result` 消息，带状态、选项和标签、答复、决定人，AI 决定的还带理由。
- 决定人默认是 `operator`，用 `--remote` 时是该远程的 `as` 名字。团队成员不能决定请示：被 spawn 的 Agent 执行 `request decide` 会以自己的名字提交，服务端拒绝。看板上的决定都以 `operator` 的身份提交。
- 只能决定 pending 的请示。例外是 AI 自动审核做出的决定：操作员可以再决定一次（覆盖），请示人会收到一条新的 `decision_result`，开头写明它覆盖了先前的 AI 决定。人做出的决定不能再改。

### AI 自动审核

按协作说明，leader 自己能定的事不再发请示，发出的请示都带 `--human`，AI 自动审核不会处理它们；它只处理不带 `--human` 的请示，比如团队说明另有约定、或手工创建的请示。

```bash
team-worker request auto my-team on --model <模型名>   # 打开；不带 --model 时沿用之前设置的模型
team-worker request auto my-team off
team-worker request auto my-team                       # 查看当前设置

# 只在每天 22:00 到次日 08:00（上海时间）自动审核
team-worker request auto my-team on --from 22:00 --to 08:00 --tz Asia/Shanghai
team-worker request auto my-team on --all-day          # 去掉时间窗口，全天审核
```

`--from`、`--to` 设每天的审核时间窗口（24 小时制 `HH:MM`），包含开始时间、不含结束时间；结束早于开始表示跨午夜，`22:00`–`08:00` 是当天 22:00 到次日 08:00。两者要么都设、要么都不设（不设就全天审核），不能相同。`--tz` 是窗口的时区，用 IANA 名称（如 `Asia/Shanghai`），不设时按 daemon 所在机器的本地时区；daemon 二进制内置了时区数据，机器或镜像里没有 zoneinfo 也能用。和模型一样，开关时不带这几个参数就沿用之前的窗口；`--all-day` 清掉窗口和时区。查看时输出窗口和此刻是否在审核，例如 `Auto review for my-team: on (model: default), 22:00-08:00 Asia/Shanghai, active now`，窗口外显示 `outside the window now`。

窗口外创建的请示不会被审，也不算审过，保持 pending；窗口打开后由 daemon 的定时轮询（30 秒一次）接着审。操作员在窗口外照常可以自己决定。

打开后，daemon 在后台把团队里还没审过的 pending 请示交给模型决定（设了时间窗口时只在窗口内），`--human` 的请示不碰。每条请示只审一次：模型选择上报（escalate）、命令失败或输出无法解析时，请示保持 pending，看板显示「AI did not decide: …」（即 `autoNote`），等人决定，不会自动重试。决定人记为 `auto`，设了模型时为 `auto:<模型名>`，理由保存在 `autoReason`。

审核命令默认是 `claude -p --tools "" --no-session-persistence`：不给模型任何工具（审核只需要读提示词，避免团队消息里的文字诱导它读本机文件），也不保存会话。可以用 daemon 的环境变量 `TEAM_WORKER_AUTO_REVIEW_COMMAND` 换成别的命令（按空格切分，不经过 shell）；设了模型时在末尾追加 `--model <模型名>`。提示词从 stdin 传入，包含团队说明、请示内容、最近 30 条团队消息和未完成的任务；单次最长 5 分钟。提示词要求模型保守：不可逆或破坏性操作（删除或移动数据、清空、重刷、改凭据）、超出团队说明授权的范围、影响团队以外的人或系统、涉及费用，或者信息不足时一律上报；否则按团队说明和约束选最合适的选项。模型最后输出一个 JSON 对象：`{"decision":"approve|reject|answer|escalate","choice":"A","reply":"...","reason":"..."}`，daemon 取输出里最后一个带 `decision` 的 JSON 对象。

## 用量与预算

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

## 团队说明与成员提示词

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

`spawn --task "..."` 会覆盖保存的专属提示词；未传 `--task` 时复用已有内容，显式传 `--task ""` 则清空。说明较长时用 `--task-file <路径>` 从文件读取，效果与 `--task` 相同（文件末尾的换行会去掉），两者不能同时使用；文件由执行命令的这台机器读取，所以远程 CLI 也能用。`launch` 会保存 TOML 中每个角色经过变量替换后的 `task`。会话恢复钩子会补回专属提示词和当前任务；`--dry-run` 不修改保存值。

## 阶段编排（harness）

```bash
# 开一轮：discuss → plan → execute → verify → ship；团队不存在时以当前身份为 leader 创建
team-worker harness start -g "给待办应用加上标签功能" -t my-team -n 3

# 让 daemon 按阶段自动拉起规划者、执行者、评审者，关卡满足后自动推进，直到这一轮结束；命令立刻返回
team-worker harness conduct my-team
team-worker harness conduct my-team --stop   # 停止调度，这一轮本身不受影响

# 手动操作
team-worker harness status my-team
team-worker harness approve my-team          # 批准规划者提交的计划（仅 leader）
team-worker harness advance my-team
team-worker harness abort my-team -r "目标有变"

# Agent 登记交付物、拆分任务时用
team-worker harness artifact my-team spec.md --file spec.md
team-worker harness artifacts my-team --show spec.md
team-worker harness contracts my-team        # 只列出名为 sprint-contract-* 的交付物
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

## 团队快照

```bash
team-worker team snapshot my-team --tag before-refactor   # 保存一份快照
team-worker team snapshots my-team                        # 列出快照，新的在前
team-worker team restore my-team 20261103T081500-before-refactor --dry-run   # 只看会恢复多少条
team-worker agent stop my-team alice                      # 恢复前先停掉团队里运行中的 Agent
team-worker team restore my-team 20261103T081500-before-refactor            # 确认后恢复；-f 跳过确认
team-worker team snapshot-delete my-team 20261103T081500-before-refactor
```

快照保存团队的配置和团队说明、预算、成员及其专属提示词、任务及依赖、未读消息、事件、原生会话记录、计划和用量明细，存在 daemon 的数据库里。快照 ID 是 UTC 时间加标签，例如 `20261103T081500-before-refactor`（标签里的特殊字符换成 `-`），同一秒内重复时加 `-2`、`-3`。命令、参数和 `--json` 字段与 ClawTeam 一致，`--json` 另外多出 `messageCount`、`planCount` 和恢复时的 `plans`、`members`。

和 ClawTeam 的不同：

- **一致性**：保存在一个事务里读取全部数据，Agent 在工作时保存也不会拿到一半的状态；恢复也在一个事务里先删后写，出错时什么都不改。
- **运行中拒绝恢复**：团队有运行中的 Agent 时，`restore` 返回错误并列出这些 Agent，先用 `agent stop` 停掉它们。正在工作的 Agent 会继续改任务、收消息，覆盖到一半会让它们的状态和数据库对不上。ClawTeam 不检查。
- **不保存**：Agent 进程记录、git worktree 和 harness 运行。它们对应的是进程和 git 里的状态，靠恢复数据库找不回来。
- **团队删除后仍可恢复**：快照不随 `team cleanup` 删除，恢复时会重建团队。不再需要时用 `snapshot-delete` 删掉。
- 由旧版本保存的快照可以在新版本恢复：只写回当前表结构里有的列，缺的列取默认值。

## 事件 hook

事件 hook 是由 `team-workerd` 在发生某个事件后执行的 shell 命令，可以用来发通知、写日志、触发 CI 等。它对应 ClawTeam 的 `hook list/add/remove/test`；因为 `hook` 已经用于 Agent CLI 自己的回调，这里放在 `event hook` 下。

```bash
team-worker event types                                    # 列出可用的事件类型和字段
team-worker event hook add -e TaskCompleted -c 'notify-send "任务 $TEAM_WORKER_EVENT_TASK_ID 完成"'
team-worker event hook add -e WorkerCrash -t my-team -c 'cat >> ~/crashes.jsonl'   # -t 只对一个团队生效
team-worker event hook list
team-worker event hook test -e WorkerCrash -t my-team -n alice   # 用一个虚构的事件立即执行，显示每个 hook 的结果
team-worker event hook remove -e WorkerCrash               # 删除该事件的全部 hook；-c 只删命令完全相同的
```

事件类型沿用 ClawTeam 的名字（能对上的）：

| 事件 | 何时触发 | 字段 |
|---|---|---|
| `AfterWorkerSpawn` | 一次 Agent 运行开始：spawn、resume、keepalive 重启 | `agent_name`、`agent_id`、`backend`、`target` |
| `WorkerExit` | 一次 Agent 运行结束，进行中的任务退回 pending | `agent_name`、`exit_code`、`abandoned_tasks` |
| `WorkerCrash` | 运行以非 0 退出码结束，且不是被要求停止的（`exit_code` 为 -1 表示退出码未知，如 daemon 发现进程已不在） | `agent_name`、`exit_code`、`error` |
| `AgentIdle` | 成员执行 `lifecycle idle` 告诉 Leader 没活了 | `agent_name` |
| `MemberJoined` / `MemberLeft` | 往已有团队加成员（含加入审批通过）/ 移除成员 | `agent_name`（加入时还有 `agent_type`、`user`） |
| `TaskCreated` | 新建任务 | `task_id`、`subject`、`owner` |
| `AfterTaskUpdate` | 每次 `task update`，以及成员退出时任务被退回 pending | `task_id`、`old_status`、`new_status`、`owner` |
| `TaskCompleted` | 任务变为 completed | `task_id`、`owner`、`duration_seconds` |
| `BudgetExceeded` | 花费第一次达到预算 | `spent_cents`、`budget_cents`、`enforce` |
| `PhaseTransition` | harness 推进或退回到另一个阶段 | `run_id`、`from_phase`、`to_phase` |
| `TeamShutdown` | 团队被删除（`team cleanup`） | 无 |

每个事件还带 `type`、`team_name` 和 `timestamp`。hook 从标准输入读到整个事件的 JSON，也能从环境变量读：`TEAM_WORKER_EVENT_TYPE`、`TEAM_WORKER_EVENT_TEAM_NAME`，以及每个字段一个 `TEAM_WORKER_EVENT_<字段名大写>`，列表用逗号连接。环境变量故意不用 `TEAM_WORKER_AGENT_NAME` 这类身份变量，hook 里调用 `team-worker` 不会冒充事件里的成员。

执行方式：

- **在 daemon 的机器上执行**：用 `--remote` 添加的 hook 也在服务器上运行，环境是 `team-workerd` 进程的环境。能调 API 的人本来就能让 daemon 启动任意命令（`spawn`），hook 不额外放宽权限。
- **不丢事件**：事件和引起它的改动在同一个事务里写进数据库，改动回滚时事件也没有。daemon 执行完一个事件的全部 hook 后才删掉它，中途重启会在下次启动时重新执行，所以 hook 可能执行不止一次，要能安全重跑。只记录有 hook 订阅的事件。
- **顺序与超时**：同一时间只执行一个 hook，事件按发生顺序处理，同一事件的 hook 按 `-p` 从小到大、再按添加先后执行；每个限时 30 秒。失败（非 0 退出、超时）只写进 `team-workerd.log`，不重试，也不影响其他 hook。
- **和 ClawTeam 的不同**：只有 shell hook，没有 Python hook；没有 `Before*` 这类能否决操作的事件；ClawTeam 的 `BeforeTaskCreate` 在这里是事后的 `TaskCreated`。`WorkerExit` 每次运行结束都会触发，ClawTeam 只在有任务被退回时触发。`MemberJoined`、`MemberLeft`、`TaskCreated`、`BudgetExceeded` 是新增的；`TeamLaunch`、`BeforeInboxSend`、`AfterInboxReceive`、`HeartbeatTimeout`、workspace 和 transport 相关的事件没有实现。
- **插件**：不提供 ClawTeam 的 `plugin list/info`。ClawTeam 的插件是进程内加载的 Python 类，唯一的内置插件 ralph loop（成员没做完任务就退出时重新拉起它）由 `spawn --keepalive` 覆盖；其他需求用事件 hook 加脚本实现。

# 团队协作流程

> 加入审批、计划审批、团队说明与成员提示词、用量与预算、阶段编排（harness）的命令和规则。

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

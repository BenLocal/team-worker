# 任务阶段、等待与轮次交接

任务状态描述任务的生命周期，阶段汇报描述当前进展。阶段汇报不会完成任务，也不会替换最终 `result`。任务状态和报告均由成员上报，系统不据此认定验收通过。

## 保存阶段进展

```bash
team-worker task progress demo <task-id> \
  --key batch-1 --summary "Finished the first batch" \
  --artifact reports/batch-1.md --verification "12 cases passed; 2 not run"

team-worker task get demo <task-id>
team-worker --json task list demo
```

`--key` 在任务内唯一。丢失响应后，用同一个 key 和相同内容重试，不会追加第二份报告；同 key 配不同内容会报冲突。新阶段使用新 key。报告按提交顺序保存，包含服务端生成的时间和调用者。`task get` 显示完整阶段历史，看板卡片默认显示最新阶段，展开后显示历史、证据和最终结果。

阶段内容写在任务中；需要成员协作或 Leader 决策时，仍按既有流程发送消息。

## 说明正在等待什么

先登记已有机制中的检查点或请示，再引用其 ID：

```bash
team-worker task progress demo <task-id> \
  --key wait-observation --summary "Prepared the comparison inputs" \
  --state waiting --reason "Waiting for the observation window" \
  --next-action "Read the sample and compare with the baseline" \
  --checkpoint <checkpoint-id>

team-worker task progress demo <task-id> \
  --key wait-decision --summary "Prepared a bounded test" \
  --state waiting --reason "The test needs a scope decision" \
  --next-action "Read the decision before taking further action" \
  --request <request-id>
```

每份等待报告关联一个触发条件。检查点必须属于当前任务和负责人，且尚未完成或取消；请示必须属于同一团队且仍为 pending。没有可登记的触发条件时，可以只写 reason 和 next-action，看板会明确提示 `No registered trigger`，不能把它当成已有定时唤醒。

任务详情、列表和看板批量读取关联记录的当前状态，不把报告时的状态当成最新状态。检查点完成或取消、请示被处理后，显示 `follow_up`，提示成员读结果并记录下一阶段。批准、拒绝和回答都只结束该次等待，不会自动执行动作或通过验收。

`nextCheckpoint` 显示最早尚未结束的检查点，即使旧任务还没有阶段报告，也能看到下一次观察时间。登记检查点不等于成员已经进入等待。恢复工作后，用新 key 追加一份 `--state active` 报告。Stop hook 和空闲提醒会放过已登记且尚未结束的等待，由原检查点或请示消息负责通知；未登记触发条件的等待仍会提醒。触发结束后恢复跟进提醒，不自动推进任务。

## 结束旧轮并交接缺口

换版、换样本或观察中断后，先创建接替任务，再结束旧轮：

```bash
team-worker task update demo <old-task-id> \
  --superseded-by <replacement-task-id> \
  --result "Observed 6 hours; remaining duration and untested cases move to the replacement" \
  --artifact reports/old-round.md --verification "Partial observation; acceptance incomplete"
```

命令将旧任务设为 `superseded`，保留阶段历史和证据、释放任务锁并取消未结束检查点。接替任务必须属于同一团队、具有相同 kind 且尚未结束，不能形成依赖环。依赖旧任务的验收会在同一事务中改为依赖接替任务，不能因为换轮而提前解锁。之后再引用旧任务作为依赖，也会沿交接记录找到当前接替任务。

交接不是通过验收。`superseded` 不计入 completed，不出现在 `task results` 的已完成交付中；看板将它放在 Closed 列并明确显示接替任务。相同交接参数可以安全重试。旧轮不能重新领取；需要更正旧报告时只更新 `--result` 等报告内容，不重复设置状态。

## 区分长期目标与本批任务

```bash
team-worker task create demo "Continuous regression coverage" --kind goal --owner leader
team-worker task create demo "Check the next release" --parent <goal-id> --owner alice
```

普通任务默认 kind 为 task。`task results` 只列普通执行任务的已完成交付，目标的结果仍可用 `task get` 查看。`task update --kind goal` 可把已有的长期职责改为目标；`--parent <goal-id>` 将本批任务关联到目标，空值可解除关联。父节点必须是同一团队中的 goal，不能形成环；已有子任务的目标不能直接改回普通任务。

目标不占执行锁，不参与普通任务的进行中、待办、已完成数量，也不会被当作无人领取的普通任务自动分配。看板单独展示目标及其直属批次的完成情况，完成子任务不会自动完成目标。

`task stats` 的 total 包含全部记录，goals 和 superseded 分开统计；completed、in_progress、pending、blocked 统计普通执行任务，waiting 是其中明确上报等待且触发尚未结束的进行中任务。默认 `task wait` 只等待普通执行任务，忽略长期目标与已交接旧轮。显式 `--task` 可等待一个目标；指定旧轮时立即报出接替任务 ID，请改为等待该任务。

## 看成员在做什么

看板成员与终端卡片分别展示进程、最近活动上报、任务去向和协议确认。进程在线只证明进程存在；未启用 hook 的执行器显示 Activity unknown，不能由此判断正在执行。活动上报附时间，便于判断新旧。

`board show` 的 `work` 来自全部任务的实时汇总：

| work.state | 含义 |
|---|---|
| standby | 没有未结束的普通执行任务，可以待命 |
| waiting | 全部任务都在等待已登记触发条件或前置任务 |
| needs_review | 有任务缺少阶段记录，或等待没有有效触发条件 |
| assigned | 有需要执行或跟进的任务，不代表进程此刻正在工作 |

下一次观察时间单独展示。只登记未来检查点、未报告本轮任务去向时仍提示 needs_review。触发到期或被取消后恢复跟进；不会提前执行未来观察。

成员停在输入框达到既有空闲阈值后，daemon 检查未结束任务的去向。Leader 也参与：团队存在未分派、可执行或去向不明的任务时，提醒其核对授权范围内可提前推进的事项；同组问题只提醒一次，持续未处理再通知操作员。全部任务有合法等待条件或已完成交付时，不强行制造工作。

## 运行中读取新版协作规则

通用提示词以嵌入模板的 SHA-256 标识版本。daemon 更新版本时，通过现有收件箱通知运行中的成员读取；相同版本重启 daemon 不重复通知。当前进程、Run ID 和任务归属保持不变。

```bash
team-worker agent protocol show demo alice --run-id "$TEAM_WORKER_RUN_ID"
team-worker agent protocol ack demo alice --run-id "$TEAM_WORKER_RUN_ID" --version <show返回的版本>
```

`show` 返回与新启动相同的角色规则，以及当前团队和个人指令。传入当前 Run ID 时保存提供读取的时间；不传 Run ID（并且环境变量为空）仅查看，不替成员登记。`ack` 必须指定当前 Run ID、当前版本，并且此前已经读取；旧进程和旧版本会被拒绝。新 Run 不继承上一轮的确认。查看 `agent list --json` 或 `board show --json` 中的 `agent.protocol` 可见 offeredAt、readAt、ackedAt。

确认记录表示成员声明读过，实际是否遵守仍须核对任务阶段和证据。协议通知复用现有消息投递和空闲唤醒机制；不启用执行器默认关闭的 hooks，也不改变其信任设置。团队或个人指令的单独修改继续走原有 prompt 更新通知。

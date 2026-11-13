# 单次检查点

检查点用于安排任务中的一次未来观察，例如构建完成后的检查或长测的一次采样。daemon 保存到期时间并投递通知，Worker 显式接手、执行检查、提交证据。检查点不会自动执行 shell 命令，也不会自动完成关联任务。

## 登记、接手和回报

任务必须有唯一、已存在的成员负责人。负责人、Leader 或 operator 可以登记检查点；负责人从任务读取，不能在登记时另行指定。

```bash
team-worker checkpoint create demo <task-id> \
  --key observation-1 \
  --at 2026-12-01T14:00:00+08:00 \
  --ack-timeout 5m \
  --result-by 2026-12-01T14:30:00+08:00 \
  --escalate-after 10m \
  --action "Read the next sample and compare it with the baseline" \
  --expect "Sample log, target version and comparison result"

team-worker checkpoint list demo
team-worker --json checkpoint show demo <checkpoint-id>
```

`--key` 是团队内唯一的登记键。请求超时后用相同键和参数重试，返回同一条记录；同一个键配不同参数会被拒绝。下一次观察使用新键。已取消的键仍保留，重试不会把它重新启动。

时间必须是包含时区偏移的 RFC3339 格式，保存和显示时转换为 UTC。`--ack-timeout` 从 `--at` 开始计算，默认 5 分钟；`--result-by` 必须晚于接手期限。`--escalate-after` 默认 10 分钟。两个时长都要求整秒，范围为 1 秒至 30 天。允许登记已到期的时间，此时会尽快通知，原截止时间不会延后。

收到通知后，由负责人执行：

```bash
team-worker --json checkpoint ack demo <checkpoint-id>
# 使用上一步返回的 token；先检查 status，已完成或已取消的观察不要重复执行。
team-worker checkpoint complete demo <checkpoint-id> \
  --token <ack-token> \
  --outcome passed \
  --summary "The sample matches the baseline" \
  --evidence "artifacts/sample.log; target build example"
```

`ack` 只对已通知的检查点生效。重复接手返回原 token；它不是锁住外部操作的机制，同一成员的多个进程仍需自行避免重复执行。`complete` 要求负责人和 token 匹配，结果必须为 `passed`、`failed` 或 `inconclusive`，并填写摘要和证据。同一结果可安全重试，不同结果不能覆盖已完成记录。无法执行检查时可提交 `inconclusive`，证据说明实际失败输出或覆盖缺口。

完成后 daemon 通知 Leader 核验证据。文件不会自动上传，证据正文也不会自动证明检查真实执行；关联任务仍保持原状态。CLI 默认使用当前成员身份（包括用户前缀），成员环境之外默认使用 operator；可用 `--caller` 显式指定身份。远程操作员模式沿用现有 `--remote` 配置。HTTP 的 `caller` 是共享 Key 信任范围内的协作身份，不是独立的成员认证。

## 超时与交接

状态按 `scheduled → notified → acknowledged → completed` 推进，未完成时也可进入 `canceled`。超时单独保存在记录中，不会把工作直接标成失败。

- 接手期限内没有 `ack`，或结果期限内没有 `complete`，通知 Leader。
- 首次告警后经过 `--escalate-after` 仍未完成或取消，通知 operator；Leader 自己负责的检查点直接通知 operator。每一级只通知一次。
- 迟到接手或迟到回报仍可接受，超时记录保留。迟到接手不撤销已经启动的升级计时；只有完成或取消才能关闭本次告警。
- Leader 读消息、屏幕变化、普通心跳和任务备注更新都不会延后检查点期限。需要调整时取消旧检查点，说明原因，再登记新检查点。
- 告警不自动按 Esc、停止进程或重启成员。operator 通知进入其收件箱，可通过 `inbox receive` 或 `task wait` 接收，不会自动发送到外部服务。

```bash
team-worker checkpoint cancel demo <checkpoint-id> --reason "The observation moved to a new task"
```

负责人、Leader 或 operator 可以取消，相关成员会收到通知。任务改派或标记完成时，同一事务取消其全部未完成检查点，并通知旧负责人。即使任务后来又改回原负责人，旧检查点仍拒绝回报；新负责人应重新登记。

当只剩未来检查点时，Worker 保存检查步骤、证据位置、后台进程和失败处理，并把检查点 ID 交给 Leader。Leader 核对记录及成员的消息接收路径后再确认交接。daemon 不会自动启动已退出的成员；成员卡在长工具调用中时也不保证立即处理通知，届时会按期限告警。

## 持久化与恢复

daemon 启动时和运行期间每秒扫描到期记录，每批最多推进 100 条。数据库中的状态变更和通知入队在同一事务完成，空扫描不唤醒消息监听器。处理是尽力及时，不是实时调度；daemon 停机期间无法执行或发告警，恢复后补处理过期记录。

检查点不依赖终端画面，也不依赖 Nudger 的空闲判定。消息取走、粘贴成功都不会自动接手。通知本身沿用现有 inbox、hook 和终端投递能力，投递链路异常时依靠未接手告警暴露问题，不承诺外部操作恰好执行一次。

团队快照包含检查点。恢复快照会恢复当时的状态和截止时间，过期记录可能立即通知；已发生的外部操作不会回滚，恢复前应评估是否需要取消旧观察。删除团队会删除其检查点。

第一版只支持单次观察。持续、精确采样应由受控脚本执行，Agent 负责结果分析；尚未增加周期调度或通用的任务进展评分。

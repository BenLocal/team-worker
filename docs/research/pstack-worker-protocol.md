# pstack 对通用 Leader / Worker 协作协议的借鉴

> 研究日期为 2026-11-13。结论来自上游原文和本地协议对照，未运行行为评估，也未修改或操作已部署团队。

## 研究范围与结论

本研究固定读取 `cursor/plugins` 的 main 提交 `6e359ce0aba9e927914d8a940510aa2ef66a8f23`。SHA 由 `git ls-remote` 获取，以下上游链接均指向该提交，避免 main 后续变化使结论失去依据。研究对象包括 poteto-mode 主技能、两个 agent 定义、Autonomous run、Orchestrate、Session pickup、Pause safely、Bug fix、Eval，以及相关原则。

最值得纳入默认提示词的是五条简短规则。它们适用于编码、研究、运维检查等不同任务；具体测试方法仍由任务决定。

1. 开工前定义可检查的完成条件，注明验收产物、允许修改的范围和权限边界。持续工作时保持原条件，不能因为反复失败自行降低标准。依据为 [Autonomous run 第 1、6 步](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/autonomous-run.md)和 [Orchestrate 的 Supervision](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md)。
2. 对实际交付物、实际版本和目标运行环境检查。将推测、静态线索、实际执行结果分开；没有执行或结果不确定的检查如实保留。依据为 [Prove It Works](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/principle-prove-it-works/SKILL.md)、[Bug fix 第 1、2、4 步](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/bug-fix.md)和 [Orchestrate 的版本验收规则](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md)。
3. 重复失败时验证共同前提，再决定缩小任务、换工具或改变策略。每轮保存尝试、观察结果和下一步，真正无路可走时报告具体阻塞。依据为 [Attack the Premise](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/principle-attack-the-premise/SKILL.md)、[Autonomous run 第 3、5、6 步](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/autonomous-run.md)及 [Orchestrate 的失败分类](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md)。
4. Leader 阅读交付物并作独立判断。Worker 的完成标记和摘要是待核验输入，Leader 应说明自己实际核验了什么。依据为 [Orchestrate 的 Topology / Supervision](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md)及 [poteto-mode 的 Subagents](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/SKILL.md)。
5. 接续工作先恢复任务、分支、产物、已验证结论、未完成事项和后台进程，再从断点继续。旧证据可复用；版本或环境变化时补做必要核验。依据为 [Session pickup 第 1 至 5 步](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/session-pickup.md)和 [Pause safely 第 4 步](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/pause-safely.md)。后台进程字段是针对本项目等待协议的适配补充。

这里的“适配建议”是研究判断，不代表上游规则能直接在本项目执行，也不代表提示词已经产生了预期行为。

## 本地覆盖情况

对照基线是研究开始时的 [spawn 提示词](../../internal/spawn/prompt.md)与 [编码行为准则](../agents-dot-md/coding-guidelines.md)。spawn 提示词已包含本轮未提交的 Waiting and background work 规则。本研究不覆盖这些改动；后续同轮实现可能已补齐下表缺口。

| 机制 | 研究开始时的覆盖 | 建议补充的位置与内容 |
| --- | --- | --- |
| 任务范围与验收 | Leader 创建任务时已要求 goal、deliverable、how to check，并指定 owner；Worker 开始前读取完整任务 | 在通用执行规则补上“开始前确认可检查的完成条件”；任务描述按需写明允许和禁止修改的范围，而非所有任务都强制填写大型表单 |
| 对真实产物验证 | 本仓库编码规范已有 prove-it-works，明确反对自述、时间戳和缓存截图；spawn 要求检查后才能完成并如实报告未执行检查 | 远程 Worker 不一定读得到本仓库规范。默认提示词保留少量核心规则，强调目标环境、实际版本以及检查结论的适用范围 |
| 重复失败后换策略 | 编码规范已有“两次同前提失败先验证前提”；通用 Worker 协议主要要求求助时说清尝试 | 将机制压缩到通用规则，覆盖非代码任务，不重复搬入整篇编码 playbook |
| Leader 独立验收 | Finish 已明确完成只代表 owner 标记完成，Leader 必须自己检查 | 补充读实际产物和证据，交付变化后旧结论需重新判断；区分 Worker 报告和 Leader 自己做过的核验 |
| 等待与责任保留 | 已有后台任务登记、处理新消息、有限阻塞等待、下一检查点、唤醒机制验证和缺口上报 | 本轮不再扩充同义规则。重点是实际工具能否实现登记和唤醒，不能由提示词文字推定已具备调度能力 |
| 交接与恢复 | 已有停止前交回任务、保存未来检查点；仓库有 recall 流程 | Worker 通用协议可补任务状态、分支与产物、检查结果、后台进程和下一步的交接要求，恢复时先读交接再继续 |
| 多人写入隔离 | spawn 已有 worktree、任务锁、禁止强抢他人任务和冲突检查说明 | 保留已有机制；提示词不能保证成员只改允许路径，也不能将任务锁等同于文件锁 |

本地编码规范已经吸收上游 prove-it-works、attack-the-premise、行为测试、幂等操作与 bug-fix 等内容。因此新增价值主要在于把必要规则送达每个 Worker，以及明确 Leader 和 Worker 之间的证据交接。再次复制整套原则会增加上下文成本。[本地规则及借鉴说明](../agents-dot-md/coding-guidelines.md)

## 原文机制与适配理由

| 原文规则及固定版本来源 | 适配建议与边界 |
| --- | --- |
| [Orchestrate 的 kickoff / Supervision](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md)列出目标、可写与不可写路径、禁止动作和完成证据；无法指出证据意味着范围尚未划清；相似任务先走通一个再铺开 | 每个单元有明确交付、依赖、权限和验收。Worker 可补充检查方式，小任务不因此增加大型规划表或审批 |
| [Prove It Works](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/principle-prove-it-works/SKILL.md)要求真实对象检查、可重跑脚本；观察失败时先检查观察方法。[Bug fix 第 1、2、4 步](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/bug-fix.md)要求匹配操作界面、运行证据确认机制、原复现通过，不确定不算通过 | 报告注明检查对象、版本或位置、执行方式、输出和未覆盖部分。研究用原始资料，故障用目标环境运行证据，不为所有任务强制截图或集成测试 |
| [Orchestrate](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md)要求作者以外的异模型 verifier，协调者读产物而非只读摘要，验收仅对当前 head 有效；[主技能 Subagents](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/SKILL.md)要求父 agent 自己审阅并总结 | 默认保留独立判断，专门 verifier 与异模型审查按风险选择。验收 commit 不能自动覆盖之后的构建和运行实例。这是版本绑定原则的适配推论，未实测 |
| [Attack the Premise](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/principle-attack-the-premise/SKILL.md)要求共同前提下反复失败后写明前提并做可区分的观察；[Orchestrate](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md)按上下文限制、连接中断、工具故障分类调整策略，核对被替换成员的迟到报告 | 保存失败证据、排除项和下一步为何不同。替换前保留未提交工作与后台检查。固定重试次数与 PR 活动信号不适合所有研究或事件等待任务 |
| [Autonomous run 第 2 步](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/autonomous-run.md)依赖 Cursor 内建 `/loop`、事件 watcher 和心跳兜底；[Orchestrate](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md)在执行器支持时订阅事件，禁止协调者轮询脚本 | 借鉴事件优先、按任务选择重查时点和明确恢复责任。上游工具不是本项目能力，不能复制命令后宣称已有调度支持；本轮等待规则无需再堆同义句 |
| [Session pickup](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/session-pickup.md)先读轨迹，恢复分支、已完成工作、待办与决策，区分 done / pending，仍需对真实产物核验；只读当前工作区相关轨迹 | 交接还应注明后台进程、下一检查点和责任人。恢复时复用有效证据，只补过期或缺失检查，避免从零重跑。不得跨项目搜索私人会话 |
| [Pause safely](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/pause-safely.md)仅在明确暂停时触发，要求安全边界和可冷启动恢复的意图、状态、验证、下一步、文件与注意事项 | 用户要求持续工作或无人值守时不能自行暂停。WIP 提交、取消子 agent、临时文件位置按本地权限与持久性决定，不原样放入默认协议 |

## 应保留为可选技能的内容

| 内容 | 原文依据 | 保留为可选的原因 |
| --- | --- | --- |
| 长期项目协调拓扑 | [Orchestrate](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/orchestrate.md) | “协调者只协调”、专门 PR owner、云环境、新任务新成员、固定协调层数和并发规模，针对跨多天的大项目。通用团队应允许单成员工作、同成员延续状态及协调者直接处理小任务 |
| 全套 bug-fix 流程 | [Bug fix](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/bug-fix.md) | 复现、运行证据和最小改动可共用；二分诊断、特定控制技能、架构评审和 PR 流程按故障复杂度启用。本地规范要求复现测试与修复同提交，不能照搬上游故意让失败测试单独进入历史的安排 |
| 暂停及恢复专用流程 | [Pause safely](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/pause-safely.md)、[Session pickup](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/session-pickup.md) | 完整流程按用户暂停或接续触发；默认提示词只需要可靠交接的最低要求 |
| 提示词行为评估 | [Eval](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/eval.md) | 评估规则变更时有价值，但普通 Worker 不必知道评估设计，也不应每个任务都启动多个候选与裁判 |
| 原样加载 poteto-mode | [poteto-agent](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/agents/poteto-agent.md)、[主技能](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/SKILL.md) | 原 agent 强制读取整个主技能及适用叶子规则，依赖 Cursor 路径、技能路由和模型配置。可借鉴派工时显式传规则入口，不能把完整风格体系设为所有成员默认环境 |
| 激进评论审查角色 | [comment-sicko](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/agents/comment-sicko.md) | 这是评论审查专用 agent，有严格保留名单与重构标记。其限制职责范围的方式可参考，默认删除倾向不适合通用 Worker |

上游 [poteto-mode 的 Autonomy](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/SKILL.md)还允许大量外部工具动作。这属于其用户的权限约定，不能据此扩大本项目成员的外部通信、发布或部署权限。应继续服从当前用户及团队指令。

## 需要代码或执行器支持的保证

[Separate Before Serializing Shared State](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/principle-separate-before-serializing-shared-state/SKILL.md)明确指出，文字约定不能承担并发控制。应先隔离可变写目标，确需共享时再用单写者、锁或原子比较等结构措施。[Encode Lessons in Structure](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/principle-encode-lessons-in-structure/SKILL.md)要求将可编码的重复纠正变成检查器或运行时结构。

下表是据此得到的工程边界判断，不是本轮实现建议清单。

| 想获得的保证 | 提示词可要求什么 | 真正保证仍需什么 |
| --- | --- | --- |
| 到未来时点恢复检查 | 保存触发时点、时区、责任人、操作和证据位置；缺少可靠唤醒时上报 | 持久化调度、触发投递、重启恢复以及执行确认。保持存活或后台 sleep 不能等同于已注册唤醒 |
| 版本变化后旧审核失效 | 报告审核对应版本，变化后补核验 | 若要强制门禁，需将审核记录绑定产物版本，并在变更时失效；自由文本 verification 字段本身不足以保证 |
| 成员互不覆盖文件 | 指定范围、分支与 owner | 工作区隔离或文件写入权限、所有权约束、冲突检测和安全合并。任务锁不会自动锁住所有相关文件 |
| 异常退出后无重复副作用 | 恢复前先读实际状态，再补差异 | 幂等操作、持久化进程与任务状态、重试关联及结果去重；重发同一段任务文字无法保证 |
| 阻塞解除后一定接续 | 写清等待对象并交接责任 | 对具体触发器验证投递、确认与恢复链路。不能把任务依赖解除机制推导成任意未来时间调度 |
| 防止虚假完成 | 要求可检查完成条件、真实证据与独立核验 | 对重要操作可增加必需证据字段或自动检查，但仍需验证证据确实对应当前产物，不能只强制填非空摘要 |

本轮没有检查所有相关实现，所以表中的“仍需什么”只说明能力类别，不判断现有代码完全缺失这些措施。研究时主线程确认尚无通用的未来 checkpoint 调度机制。后续已实现[单次检查点](../checkpoints.md)的持久化通知、显式接手与超时升级；它仍不保证模型立即响应，也不自动执行外部观察。

## 后续验证建议

[Eval](https://github.com/cursor/plugins/blob/6e359ce0aba9e927914d8a940510aa2ef66a8f23/pstack/skills/poteto-mode/playbooks/eval.md)建议先定义行为标准，再准备看起来像真实用户任务的请求。候选不应知道评估意图、其他候选或评分表；裁判看到匿名结果；同一裁判用同一尺度比较变体。最后从真实轨迹和产物核对行为，协调者还要自己阅读结果，不能只采用评分或自述。

若验证默认提示词改动，可以用五类小任务观察行为，先不增加固定模型阵容或大型评分流程。

- 任务缺少验收方式，观察成员是否补出可检查理解，并守住授权范围。
- 给出可信但已过期的运行结果，观察是否核对当前版本并保留未实测说明。
- 两轮相似尝试均失败，观察是否验证共同前提，而非第三次沿用同一假设。
- Worker 交付摘要声称通过，但产物存在可直接发现的缺口，观察 Leader 是否自己读取并识别。
- 交接中包含已完成产物及未完成后台检查，观察接续成员是否避免重复启动、恢复责任并完成后续检查。

检查应看实际工具行为、产物和状态转换。仅在提示词测试中断言出现了某句话，只能证明文字被注入，不能证明成员会执行该规则。本研究已核对来源与本地规范，尚未执行这些行为验证。

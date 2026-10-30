# AGENTS.md

> 本文件是 Claude Code 唯一自动加载的项目契约（`CLAUDE.md` 软链到此）。**核心准则写在本文件**；细化规范拆到 `docs/agents-dot-md/*`、经验沉淀放 `docs/memory/*`（都不自动加载，按需查阅）。下方《项目 Skill 索引》《模块文档索引》《记忆索引》由 agents-dot-md skill 的 `reindex.py` 生成，勿手工编辑标记之间的内容。

## 工作区规则
- 作用范围：本规则适用于当前仓库及所有子目录。
- 交流语言：默认使用简体中文；仅在用户明确要求时使用英文（详见 `docs/agents-dot-md/translation.md`）。
- 代码注释：源码中的注释（含 doc comment）、日志、错误信息和 CLI 帮助文本一律使用英文；文档仍用中文。
- 修改原则：仅做最小、精确改动，避免无关重构。
- 安全原则：未经明确授权，不执行破坏性 git / 文件操作。
- 验证要求：代码改动后至少跑 `make fmt && make vet && make test`，并发路径补跑 `go test -race ./...`；汇报完成前按 `docs/agents-dot-md/coding-guidelines.md`《完成前先证明它真的能用》说明证据。
- 接续工作：用户说「继续」而当前会话没有上文时，先用 `/agents-dot-md recall` 从最近的会话和 git 现场重建进度，不要让用户重述。
- 自检要求：代码改动后必须逐条对照 `docs/agents-dot-md/code-checklist.md`（多语言仓库还有其中索引的 `code-checklist/<lang>.md`），全部符合才算完成。

## 编码行为准则（Karpathy 防错指南）
1. **先想后写**：不臆测、不藏困惑；多种解读先摆选项让用户拍板；有更简单实现就直说。
2. **简单优先**：只写当前所需最小代码，不提前抽象、不加没要求的防御。
3. **外科手术式改动**：只动该动的；不顺手重构没坏的；只清理本次改动产生的孤儿。
4. **目标驱动**：把任务转成可验证目标（先写复现 / 非法输入测试再改），多步任务先给带验证点的计划。

> 可执行细则与「改动落地规则」见 `docs/agents-dot-md/coding-guidelines.md`。

## 代码 checklist（强制）
> ⛔ 每次写完 / 改完代码，**必须逐条对照 `docs/agents-dot-md/code-checklist.md` 自检**；多语言仓库的语言专项拆在 `code-checklist/<lang>.md`（`code-checklist.md` 里有索引），改了哪种语言就同时对照哪份。全部符合才算完成；不符合的先改到符合，不要留给评审或线上发现。通用条目追加到 `code-checklist.md`，某种语言的条目追加到该语言的文件。

## 架构与技术栈
> 动结构 / 加模块 / 接外部依赖前读 `docs/agents-dot-md/architecture.md`（组件边界、分层、关键链路、设计决策）；
> 写代码前读并遵循 `docs/agents-dot-md/tech-stack.md`（技术选型、数据访问与配置约定、构建与验证命令）。与本文件冲突时以本文件为准。

## 技能整理（Skill 维护）
本仓库自带的项目 skill 放在仓库任意位置的 `skills/<name>/SKILL.md`（含各业务模块子目录下的 `skills/`；YAML frontmatter 至少含 `name` / `description`，可带脚本 / 资源同目录）。它们随仓库分发、对所有克隆生效；全局 skill（`~/.claude/skills/*`）不入库，本索引不收录。

- 新增 / 改名 / 删除 skill，或改了 `SKILL.md` 的 `description` 后，在仓库根运行 `python3 ~/.claude/skills/agents-dot-md/scripts/reindex.py .`（Windows 用 `python`）重建下方《项目 Skill 索引》《模块文档索引》与 `docs/agents-dot-md/00-index.md`。
- **不要手工编辑** `<!-- SKILLS:START -->…<!-- SKILLS:END -->` 与 `<!-- MODULES:START -->…<!-- MODULES:END -->` 之间的内容——会被脚本覆盖。
- `description` 写清「何时用 / 触发词」，首句作为索引摘要（脚本取首句）；触发要精准，避免与既有 skill 语义重叠。
- 任务中途发现某个 skill 坏了（步骤过时、命令跑不通、引用的文件不存在），单独修好它，不要悄悄绕过去，否则下一个 agent 还会再踩一遍；修复和当前任务分开提交。

## 记忆记录（Memory）
本仓库的记忆区是 `docs/memory/*.md`——**纯 Markdown，不依赖外部记忆服务，也不需要 LLM key**，你自己用读写文件的工具维护，检索时直接 `grep` / 读文件。

- **何时记**：完成一次排查 / 根因分析 / 踩坑修复后，把「非显然、下次能省事」的结论写下来，别让下一个会话重新推导。
- **记在哪**：按主题聚合到一个文件（如 `docs/memory/build-and-deploy.md`、`docs/memory/known-pitfalls.md`、`docs/memory/external-integrations.md`），不要一条一个文件。文件头两行必须是 `# 标题` 与 `> 一句话摘要`（否则进不了索引）。
- **和记忆服务的分工**：本环境另有记忆服务（如 mem0）时，只和本仓库有关的结论写这里，随 git 共享给团队和其他机器；跨项目的个人偏好和本机环境事实（代理、凭据放在哪、本机工具版本）写记忆服务，没有记忆服务就不记，不要写进仓库。同一条事实只写一处。
- **每条怎么写**：一行一条，**绝对日期**打头，写清「现象 → 原因 → 结论 / 做法」；能附证据就附（`文件:行`、命令、报错原文）。结论标明确定程度：实测过或有原文为据的直接写；由几条间接证据推出的标「佐证」并列出证据；推断和推测标「待验证」并写出推理；查过没找到的写明查了哪里。
- **不记什么**：代码结构、git 历史、本文件或模块里已写过的内容；凭据 / 密钥只落未入库的本地文件（如 `dev-env.local.md`），**绝不**写进 `docs/memory/` 或任何入库文件。写进 `docs/memory/` 的内容先脱敏：邮箱、手机号、真实姓名换成角色，本机路径写成 `~/…`，摘录的日志去掉 token 和用户数据。
- 增删主题文件后在仓库根运行 `python3 ~/.claude/skills/agents-dot-md/scripts/reindex.py .` 重建下方《记忆索引》。与某个任务关联的结论，可按需另记到任务系统（如 `vikunja`）的评论里。

## 规则复盘（reflect）
- 本会话里用户对同一类问题纠正了 2 次以上，或用户说「复盘 / 反思最近的对话」时，提议用 agents-dot-md skill 的 reflect 流程（`/agents-dot-md reflect`，步骤见 skill 内 `references/reflect.md`）把纠正收敛进本文件、checklist 或 `docs/memory/`；用户同意后再执行。
- reflect 写回的内容必须脱敏，`reflect.py apply` 会拦下疑似敏感的修改。

## 📇 项目 Skill 索引（全仓 SKILL.md，脚本生成）
<!-- SKILLS:START -->
- **team-worker** — 用 team-worker CLI 组织多 Agent 团队：建团队、拆分并分派任务、启动 worker Agent、收发 inbox 消息、等待全部任务完成 （`skills/team-worker`）
- **teamworker** — 从团队外部远程操作已部署的 team-worker 多 Agent 团队（默认 115 上的部署，替代原 ClawTeam OpenAPI）：查看团队和看板、给成员派任务、给 Leader 发消息、等… （`skills/teamworker`）
<!-- SKILLS:END -->

## 📂 模块文档索引（docs/agents-dot-md/，脚本生成）
<!-- MODULES:START -->
- [系统架构](docs/agents-dot-md/architecture.md) — 组件边界、关键链路、数据流与设计决策；动结构、加模块、接外部依赖前先看本文。
- [代码 checklist（强制自检）](docs/agents-dot-md/code-checklist.md) — 写完 / 改完代码必须逐条对照的硬性自检清单；新增条目直接追加本文件。
- [编码行为准则（Karpathy 防错指南 · 展开）](docs/agents-dot-md/coding-guidelines.md) — 写 / 改 / 审代码的可执行细则；AGENTS.md 顶层只留四条高压线，细则在此。
- [开发环境 / 推送 / 发布](docs/agents-dot-md/environment.md) — 本地开发数据目录隔离、Git 推送、安装发布。
- [技术栈与实现规范](docs/agents-dot-md/tech-stack.md) — 技术选型、数据访问与配置约定、构建与验证命令；写代码前先看本文，按既有方式扩展。
- [交流与语言约定](docs/agents-dot-md/translation.md) — 默认简体中文交流与文档；代码中的注释、日志和错误信息一律使用英文。
<!-- MODULES:END -->

## 🧠 记忆索引（docs/memory/，脚本生成）
<!-- MEMORY:START -->
- [Agent 会话恢复的参考实现问题](docs/memory/agent-lifecycle.md) — 对照 ClawTeam 时需要注意的会话标识、退出清理和重启判定。
- [看板终端显示问题](docs/memory/known-pitfalls.md) — Ghostty 终端的浏览器光标、自动高度与网格滚动的排查结论。
- [Web 访问鉴权与反向代理](docs/memory/web-access.md) — 看板登录经过 Nginx 路径前缀时的跳转约定和实测问题。
<!-- MEMORY:END -->

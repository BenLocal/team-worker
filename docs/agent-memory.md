# 成员个人文档与复盘

个人文档保存在 daemon 所在机器的 `<数据目录>/agent-data/`。目录按用户、团队和成员名称编码，独立于任务工作目录。正文是 Markdown，历史与运行元数据是本地文件，不写入 SQLite。

同一用户、同一团队下重建同名成员会继承资料。团队清理也保留这些文件；团队和成员改名按新身份处理。备份时必须单独包含个人资料根目录，数据库快照不包含这些文件。

## 文档操作

```bash
team-worker memory init demo worker
team-worker memory list demo worker
team-worker --json memory read demo worker MEMORY.md
team-worker memory write demo worker MEMORY.md '# Memory' --base-hash '<当前哈希>'
team-worker memory write demo worker notes/lesson.md --file ./lesson.md --base-hash ''
team-worker memory search demo worker '关键词'
team-worker memory history demo worker MEMORY.md
team-worker memory rollback demo worker MEMORY.md '<历史版本>' --base-hash '<当前哈希>'
```

所有命令支持 `--user` 及全局 `--remote`、`--json`。`--file` 从客户端读取内容，实际文档操作由服务端执行。写入和回滚必须提供基础哈希；空哈希仅用于新建文件。检测到版本冲突时拒绝写入。接口间的写入串行执行；直接编辑器不遵守文件锁，应用复盘时仍应避免同时编辑同一文档，普通文件系统无法提供对任意外部编辑器的原子版本比较与替换。

受管文件包括 `AGENTS.md`、`MEMORY.md`、`SCRATCHPAD.md`，以及 `daily/`、`notes/` 下的 Markdown。接口拒绝目录穿越和符号链接。每篇正文上限 256 KiB，使用 UTF-8。可以直接编辑这些文件，daemon 在检查时归档看到的版本，不能保证捕获两次检查之间的每次修改。

历史保存在 `history/`，配置、资料身份、会话引用、文件哈希和复盘状态保存在 `.state/` 或资料根目录的隐藏文件中。这些目录不属于项目仓库。历史目前不自动清理，长期运行时应监测占用。系统用户权限相同的进程仍可能访问其他目录，本功能不提供操作系统级隔离。

## 加载与状态

启动和恢复都重新提供个人上下文，使用绝对路径引用资料，不往项目目录复制个人规则。Claude Code 和 Pi 通过会话恢复、压缩及工具和轮次钩子检查更新；Codex 默认关闭 hooks，主要通过启动和恢复加载，需要运行中更新时可手动执行下方 context 命令，或配置已有的 hooks 适配能力。

```bash
team-worker memory context demo worker
team-worker memory status demo worker
team-worker memory ack demo worker --run-id '<当前运行标识>' --hashes '<路径到当前哈希的 JSON>'
```

`status` 区分当前版本、已提供的文件清单和成员确认读取的版本。`ack` 要求提交当前运行及全部当前文件哈希，旧运行或旧文件版本会返回冲突。读取确认只代表成员的声明，是否遵守规则仍需看实际任务产物。

没有提供运行标识的操作者查看不会消费成员的待加载更新。

默认上下文预算为 24 KiB。规则或记忆超出预算时会提供文件路径及明确的完整读取要求，不截断后假装已经加载。其他日志与专题文档按需读取。个人规则须服从用户当前指令和项目强制约束。

## 复盘

复盘从已关联的精确会话、每日记录、专题文档及该成员的任务结果报告收集证据，只使用人类与助手消息，不把工具结果直接提升为规则。支持 Claude Code、Codex、Pi 的 JSONL 会话。历史会话引用随资料保留，但原始会话文件仍由执行器管理，移动或删除它们后不能再从这些文件收集证据。

输入有大小预算，只保留最近的会话消息；同一输入已处理过时跳过模型调用。配置允许的目标文档以当前内容和哈希传给独立批处理执行器，执行器输出候选 JSON，再由文件模块校验和应用。任务结果报告标明为成员自述，任务归属在多个用户间有歧义时跳过。模型和凭据沿用配置的运行 profile，daemon 不增加 LLM API 客户端。

```bash
team-worker memory reflect-config demo worker
team-worker memory reflect-config demo worker --file ./reflection.json
team-worker memory reflect demo worker
team-worker memory reflect-status demo worker
team-worker agent stop demo worker
team-worker memory reflect-apply demo worker '<候选版本>'
team-worker memory reflect-discard demo worker '<候选版本>'
```

配置文件示例：

```json
{
  "enabled": true,
  "autoApply": true,
  "hour": 3,
  "timezone": "Asia/Shanghai",
  "profile": "reflection",
  "targets": ["AGENTS.md", "MEMORY.md"],
  "timeoutSeconds": 300
}
```

`profile` 指已有的运行 profile，也可改用 `command` 数组，两者不能同时指定。两者均省略时，优先采用该成员当前保存的原始启动命令，否则采用默认 profile。包含续接参数的命令会被拒绝，应为复盘另配一个启动新会话的命令。

自动调度默认关闭。启用后按指定时区在每日指定小时之后检查，成功处理后当天不再调用，失败每天最多尝试三次。单次超时默认 300 秒，最多 600 秒；stdout 上限 1 MiB。没有新证据时不调用模型。daemon 重启后读取文件状态继续工作。

`autoApply` 默认关闭。启用后，候选修改只在成员已停止、keepalive 不会继续启动时应用，与该成员的启动过程互斥。运行中的成员保留候选，停止后再应用。超过 1 KiB 的文档若被删掉一半以上，自动应用会保留候选供手工检查。应用前检查全部基础哈希；冲突保留候选及当前正文。多文件应用若中途失败会记录 `partial`，重试跳过已经写入的相同内容，不报告整批成功。

复盘在临时目录运行，不获得成员的协调身份或 team-worker hooks。Claude Code 禁用内置工具及 MCP，Pi 禁用工具和扩展发现，Codex 使用只读沙箱。自定义命令的行为由操作者配置负责；提示词要求执行器只返回候选，不应把这当作通用进程安全沙箱。

## 全局配置

daemon 的 `config.json` 可设置：

```json
{
  "memory": {
    "root": "<个人资料绝对路径>",
    "context_bytes": 24576,
    "poll_seconds": 10
  }
}
```

`root` 默认是 `<数据目录>/agent-data`。上下文预算小于 1024 字节时使用默认值，检查周期不在 1 至 86400 秒内时使用默认 10 秒。配置在 daemon 启动时读取，修改后重启生效。更换根目录不会自动迁移旧文件。

## 验证

文件、API、CLI 和调度测试随 `make test` 运行。真实执行器验收可执行：

```bash
python3 scripts/verify-agent-memory.py --engine claude --with-reflection --bin-dir ./bin
python3 scripts/verify-agent-memory.py --engine codex --bin-dir ./bin
python3 scripts/verify-agent-memory.py --engine pi --bin-dir ./bin
```

脚本使用已安装 CLI 的模型与登录配置，复制必要配置到临时目录，分别检查跨项目目录、更新规则后恢复旧会话，以及团队重建后的继承。验收会调用真实模型。结束时删除复制的认证配置，保留测试产物和报告；它不安装或修改执行器配置。压缩和长期运行中的更新另需按执行器实际能力验收，脚本没有覆盖这两项。

当前验证结果见 [验收记录](agent-memory-validation.md)。

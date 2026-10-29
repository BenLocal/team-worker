# Agent 适配器与环境变量

`team-worker spawn` 和 `launch` 并不认识具体的 Agent CLI。启动哪个 CLI、怎么传参数，都交给**适配器**决定：权限参数怎么加，第一条提示词怎么送进去，需要哪些环境变量，启动时会弹出哪些确认画面、该按什么键。

项目自带几种常见 CLI 的适配器。其他 CLI，或者想改内置适配器的行为，都在 `~/.team-worker/agents/<name>.toml` 里声明，不用改代码。

## 怎样选中适配器

命令的可执行文件名（`--` 后面的第一个词，只看文件名，不看路径）会依次和下列适配器比较，用第一个匹配上的：

1. `~/.team-worker/agents/*.toml`，按文件名排序；
2. 内置适配器；
3. `generic`：不加任何权限参数。tmux 后端下，CLI 启动后把提示词粘贴进终端；subprocess 后端下，提示词从 stdin 传入。

想知道最终选中了哪个适配器、实际会执行什么命令，用 `team-worker spawn ... --dry-run` 查看，它不会启动任何东西。

## 内置适配器

| 名字 | 可执行文件 | 跳过权限确认 | tmux 下的提示词 | subprocess 下的提示词 |
|---|---|---|---|---|
| `claude` | claude、claude-code | `--dangerously-skip-permissions`（以 root 运行时不加） | 作为参数 | `-p` |
| `codex` | codex、codex-cli | `--dangerously-bypass-approvals-and-sandbox` | 作为参数 | 作为参数，并自动补上 `exec` 子命令 |
| `gemini` | gemini | `--yolo` | `-i` | `-p` |
| `kimi` | kimi | `--yolo` | `--print -p` | `--print -p` |
| `qwen` | qwen、qwen-code | `--yolo` | 粘贴 | stdin |
| `opencode` | opencode | `--yolo` | 粘贴 | stdin |
| `dsh` | dsh | 环境变量 `DSH_PERMISSION_MODE=danger-full-access` | 作为参数 | 作为参数 |
| `pi` | pi | `--approve`（pi 本身没有工具审批，这个参数让 pi 在本次运行中信任工作目录里的 pi 扩展、技能等文件） | 作为参数 | `-p` |

`claude` 适配器还会去掉 `CLAUDECODE`、`CLAUDE_CODE_ENTRYPOINT`、`CLAUDE_CODE_SESSION`。不去掉的话，leader 本身跑在 Claude Code 里时，启动的 worker 会以为自己是嵌套运行。`claude`、`codex`、`gemini` 还会自动确认首次进入目录时弹出的信任提示。

`spawn --no-skip-permissions` 会关闭"跳过权限确认"这一列，其余各列不受影响。

## 适配器文件格式

```toml
# ~/.team-worker/agents/aider.toml
name = "aider"                    # 可省略，默认取文件名
match = ["aider"]                 # 由这个适配器处理的可执行文件名
skip_permissions = ["--yes-always"]
skip_permissions_env = {}         # 跳过权限确认时额外设置的环境变量
prompt = "flag:--message"         # 第一条提示词的传法，同时用于两种后端
# prompt_tmux = "paste"           # 也可以按后端分别设置，优先级高于 prompt
# prompt_subprocess = "stdin"

inherit = true                    # false：不继承调用者的环境变量，只保留 pass 里列出的
pass = ["PATH", "HOME", "LANG"]
unset = ["OPENAI_ORG_ID"]         # 从环境中去掉的变量
[env]                             # 要设置的变量，值里可以用 ${NAME}
AIDER_MODEL = "${TEAM_AIDER_MODEL}"

nudge = true                      # false：不让 daemon 往这个 CLI 的终端里打字提醒（见下文）
# hooks = true                    # 仅在 extends 了 claude、codex 或 pi 时可用：开启或关闭 team-worker 的 hook 或扩展

[[startup]]                       # 启动画面规则，可以写多条
all = ["open documentation url"]  # 画面里必须同时出现这些文字（不区分大小写）
any = [["(y/n)", "[y/n]"]]        # 每组至少要出现一个
keys = ["n", "Enter"]             # 依次按下的 tmux 按键名
```

`prompt` 的取值：

| 值 | 效果 |
|---|---|
| `arg` | 把提示词作为最后一个参数 |
| `flag:<参数>` | 先加上这些参数，再接提示词。可以写多个，用空格分开，例如 `flag:--print -p` |
| `paste` | CLI 启动后，把提示词粘贴进它的终端（仅 tmux 后端） |
| `stdin` | 从标准输入传入（仅 subprocess 后端） |

**`startup` 规则**的匹配方式：tmux 后端启动后，team-worker 每 0.3 秒读取一次窗格画面。画面满足某条规则，就按下这条规则里的 `keys`；画面连续一秒左右不再变化，就不再检查。规则按文件里的顺序检查，用户写的规则排在继承来的规则前面。

**文件里有拼错的字段，或者有不认识的字段，都会直接报错**，不会被悄悄忽略。

### 在内置适配器基础上调整

写上 `extends = "<内置名>"` 后，适配器会继承这个内置适配器的全部行为，包括内置代码里的特殊处理，比如 codex 自动补 `exec` 子命令。文件里写了的字段才会覆盖继承来的设置，其中 `env`、`pass`、`unset`、`startup` 是在继承的基础上追加。省略 `match` 时，沿用内置适配器的可执行文件名，这个文件就会取代内置适配器生效：

```toml
# ~/.team-worker/agents/codex-gateway.toml：所有 codex Agent 都走公司网关
extends = "codex"
unset = ["OPENAI_ORG_ID"]
[env]
OPENAI_BASE_URL = "https://gateway.internal/v1"
OPENAI_API_KEY = "${GATEWAY_KEY}"
```

如果只想对一部分 Agent 生效，就给这个适配器起一个不同的可执行文件名（例如 `match = ["codex-gw"]`，再把 `codex-gw` 做成指向 codex 的软链接），或者改在模板里给对应的 Agent 设置 `env`。

## 环境变量怎样合成

Agent 的环境变量按下表顺序一层层合成，后面的层覆盖前面的层：

| 顺序 | 来源 | 说明 |
|---|---|---|
| 1 | 调用 `spawn` 的进程的环境 | 适配器设了 `inherit = false` 时，只保留 `pass` 里列出的变量 |
| 2 | team-worker 的通用修正 | `TERM` 是 `dumb` 或为空时改成 `xterm-256color`；去掉 `TMUX` 和 `TMUX_PANE` |
| 3 | 适配器 | 内置适配器的设置，或者 TOML 文件里的 `env` 和 `unset`；跳过权限确认时，再加上 `skip_permissions_env` |
| 4 | 模板的 `[template.env]` | 只在 `launch` 时生效 |
| 5 | 模板中某个 Agent 的 `env` | 只在 `launch` 时生效 |
| 6 | `--env KEY=VALUE` | `spawn` 和 `launch` 都可以用，可以写多次 |
| 7 | team-worker 身份变量 | `TEAM_WORKER_AGENT_ID`、`TEAM_WORKER_AGENT_NAME`、`TEAM_WORKER_AGENT_TYPE`、`TEAM_WORKER_TEAM_NAME`、`TEAM_WORKER_AGENT_LEADER`、`TEAM_WORKER_DATA_DIR`、`TEAM_WORKER_SOCKET`、`TEAM_WORKER_WORKSPACE_DIR`、`TEAM_WORKER_BIN`，以及有设置时的 `TEAM_WORKER_USER`。前面任何一层试图设置这些变量都会报错 |
| 8 | `PATH` | 在最前面加上 team-worker 所在的目录，确保 Agent 能找到 `team-worker` |

规则：

- 值里的 `${NAME}` 取自**调用者**的环境变量。如果这个变量没有设置，启动会直接失败，并在报错里给出变量名，不会替换成空字符串。这样密钥可以只放在调用者的环境里，模板和适配器文件提交到仓库也没问题。
- tmux 后端会先清空 tmux 服务器自带的环境，再写入合成结果，所以 `unset` 掉的变量一定不会出现在 Agent 里。合成结果写在一个权限为 0600 的临时文件里，窗格读取后立即删除。
- team-worker 不会把环境变量的值存进数据库。`--dry-run` 的输出里也只有变量名和它来自哪一层。

```bash
team-worker spawn tmux --team demo -n alice --dry-run -- codex
# Adapter:   codex-gateway (~/.team-worker/agents/codex-gateway.toml)
# Command:   codex --dangerously-bypass-approvals-and-sandbox <prompt: 1834 chars>
# Environment (values hidden):
#   OPENAI_API_KEY          adapter codex-gateway (...)
#   TEAM_WORKER_AGENT_NAME  team-worker
#   ...
```

`launch` 在创建团队之前，会先对每个 Agent 做一次同样的检查。命令找不到，或者 `${NAME}` 没有设置，都会直接失败，不会留下建了一半的团队。

## Hook：让消息和任务自动送到 Agent 面前

LLM Agent 不会自己去查收件箱。一轮回复结束、停在输入框之后，发给它的消息就没人看了。支持 hook 的 CLI 可以把这件事交给 `team-worker hook`：每次 Agent 准备停下、调用完一个工具、或者会话被重新开始时，CLI 都会调用 team-worker，由 team-worker 决定要不要让 Agent 继续，以及要给它看哪些内容。

内置的 `claude` 和 `pi` 适配器默认会接入团队协作：claude 通过 hook，pi 通过扩展（见下文"pi 扩展"）。内置的 `claude` 适配器默认会安装 hook。spawn 时，它通过 `claude --settings '<json>'` 把 hook 只加到这一个会话上，不改用户的 `~/.claude/settings.json`；用户自己配置的 hook 照常生效。`codex` 适配器也支持 hook，但默认关闭，原因见下文"Codex 的 hook"。

| Claude Code 事件 | 调用的命令 | 作用 |
|---|---|---|
| `Stop` | `team-worker hook stop` | 见下文 |
| `PostToolUse` | `team-worker hook post-tool-use` | 每 15 秒最多检查一次收件箱。有新消息时，作为附加上下文交给 Agent，所以它在干活途中也能收到 leader 的回复 |
| `SessionStart`（startup、resume、clear、compact） | `team-worker hook session-start` | 启动时捕获原生会话 ID；会话恢复、清空或上下文被压缩后，重新提供身份、工作目录、团队说明、专属提示词、当前任务和协作规则 |

`hook stop` 按顺序做以下判断：

1. 收件箱里有未读消息：阻止 Agent 停下，把消息内容作为让它继续的理由。如果其中有关闭请求，同时提示它怎样批准关闭。
2. **worker**：自己名下还有 pending 或 in_progress 的任务时，阻止停下并列出这些任务。如果这一轮本来就是被 Stop hook 拦下来才继续的（`stop_hook_active`），就不再催促，免得它停不下来。
   **leader**：团队的任务已经全部完成时，允许停下。
3. 以上都不满足，就等待新消息或任务变化，worker 最多等 300 秒，leader 最多等 600 秒。期间收到新消息、自己被阻塞的任务变成可做、被分配了新任务，或者（对 leader 来说）任何任务的状态变了，就阻止停下，并把这些变化告诉 Agent。
4. 等到时间也没有变化，就允许停下。worker 停下前会通知 leader 一次"空闲"，连续空闲期间不会重复通知。

这样 leader 不需要再执行 `task wait` 这种一等就是很久、期间看不到任何消息的命令：每次它准备停下时，worker 的求助和任务进展都会直接交到它手上。

hook 命令不论成败都以退出码 0 结束。daemon 连不上或输入格式不对时，它什么也不输出，Agent 照常停下或继续工作，不会被 team-worker 的问题卡住。

### 已知限制

- Claude Code 对 Stop hook 的连续拦截有上限：Agent 没有调用工具、连续被拦 8 次后，它会强制停下。正常情况下，Agent 收到消息后会去调用 `team-worker` 等工具，计数随之清零。可以用 `CLAUDE_CODE_STOP_HOOK_BLOCK_CAP` 调大这个上限，例如在适配器里写 `[env]`。
- Stop hook 等待期间，Claude Code 的界面会显示 "Waiting for team messages and tasks..."。这段时间里还能不能在终端输入，文档没有说明，以实际使用为准。
- Agent 等待超时、已经完全停在输入框之后，hook 不会再被触发。这种情况由下一节的终端提醒接手。
- 不想安装 hook 时，写一个适配器：`extends = "claude"` 加上 `hooks = false`。

### 给自己启动的会话装上 hook

如果你在自己的终端里用 Claude Code 当 leader，可以手动装上同样的 hook：

```bash
export TEAM_WORKER_TEAM_NAME=demo TEAM_WORKER_AGENT_NAME=leader
claude --settings "$(team-worker hook settings --role leader)"
```

`team-worker hook settings` 会打印 spawn 使用的那段 settings JSON，其中引用的是当前 team-worker 二进制的绝对路径。

### Codex 的 hook（实验性，默认关闭）

Codex 的 hook 和 Claude Code 使用同样的事件名和 JSON 格式，所以 team-worker 的 hook 命令可以直接给它用。区别在于 Codex 只执行**已信任**的 hook：如果要只给某一次会话加上 hook，就得同时加上 `--dangerously-bypass-hook-trust`。在 Codex 0.164 上实测，不加这个参数时，用 `-c hooks.*` 声明的 hook 不会执行。加上这个参数后，hook 能不能生效，作者还没能实测。

要试用，写一个适配器：

```toml
# ~/.team-worker/agents/codex.toml
extends = "codex"
hooks = true
```

启用后，codex Agent 的启动命令里会多出 `--dangerously-bypass-hook-trust` 和三个 `-c hooks.Stop=…`、`-c hooks.PostToolUse=…`、`-c hooks.SessionStart=…` 参数，可以用 `--dry-run` 查看。`--dangerously-bypass-hook-trust` 会让这一次会话跳过**所有** hook 的信任检查，包括你自己在 `~/.codex/hooks.json` 里配置的 hook，开启前请确认这是你能接受的。验证方法：用 `spawn tmux … -- codex` 启动一个 Agent。如果 Codex 的界面里出现 `hook: Stop`，并且 Agent 停下时能收到新消息，就说明 hook 生效了。

### pi 扩展

pi 不支持 hook，但可以加载 TypeScript 扩展。team-worker 的二进制里内置了一个扩展。spawn 时，它会把这个扩展写到 `<数据目录>/run/pi/team-worker.ts`，再用 `pi -e <路径>` 加载。扩展本身不包含协作逻辑，只在 pi 的几个时间点调用对应的 `team-worker hook` 命令，所以它和 Claude Code 的 hook 遵循同一套规则：

| pi 事件 | 调用的命令 | 作用 |
|---|---|---|
| `agent_settled`（这一轮结束，Agent 空闲） | 循环调用 `team-worker hook stop --wait 50` | 有新消息、可做的新任务，或者（对 leader 来说）团队有进展时，用 `pi.sendUserMessage` 开启新的一轮，不需要往终端里打字 |
| `agent_start` | 无 | 停止空闲循环 |
| `tool_execution_end` | `team-worker hook post-tool-use` | 干活途中收到的消息，以 `steer` 方式插进当前这一轮 |
| `session_start`（resume、fork、new）、`session_compact` | `team-worker hook session-start` | 以 `nextTurn` 方式附上身份、团队说明、专属提示词和当前任务，在下一轮生效 |

和 Claude Code 相比，pi 能**主动**开启新的一轮。所以 pi Agent 停在输入框时也会被扩展唤醒，用不上终端提醒。终端提醒仍然开着，只是作为备用：扩展没有加载成功时，还有它兜底。

扩展只在交互模式（tmux 后端）和 RPC 模式下运行空闲循环。print 模式（subprocess 后端会用 `-p`）在这一轮结束后就会退出，所以只会恢复上下文，不会等待新工作。扩展需要找到 team-worker 的可执行文件：spawn 会通过 `TEAM_WORKER_BIN` 传入绝对路径；如果没有这个变量，就在 `PATH` 里查找 `team-worker`。不想加载这个扩展时，写一个适配器：`extends = "pi"` 加上 `hooks = false`。

## 终端提醒：唤醒停在输入框的 Agent

hook 只在 Agent 这一轮还没结束时起作用。Agent 已经停在输入框等人输入时，什么都不会触发它，发给它的消息只会留在收件箱里。为此，daemon 每 2 秒检查一次所有 tmux 后端的 Agent。如果某个 Agent 同时满足下面三个条件，daemon 就往它的终端里输入一行提醒并回车：

1. 收件箱里有未读消息；
2. 终端画面已经 8 秒没有变化。正在工作的 CLI 会刷新进度动画，所以它的画面一直在变；
3. 画面最后几行是输入提示符（`>`、`❯` 或 `›`，允许外面套一层边框）。权限确认框、选择菜单这类画面没有输入提示符，daemon 不会往里面打字，所以不会误按回车替你做选择。

提醒内容是：`You have N new team-worker message(s) in team <team>. Run team-worker inbox receive <team> and handle them.`。这行字相当于一次新的用户输入，Agent 会开始新的一轮，hook 也随之重新生效。同一批消息只提醒一次；有新消息到达时，距离上次提醒至少隔 20 秒才会再提醒；消息一直没读的话，每 5 分钟再提醒一次。

这个机制对所有 tmux 后端的 CLI 默认开启，包括没有 hook 的 CLI。subprocess 后端没有终端，所以不提醒。如果某个 CLI 的输入方式和上面的假设不一样（比如它的提示符不是这几个字符，或者往里面打字会产生副作用），在适配器里写 `nudge = false` 关掉即可。

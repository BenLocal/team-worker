# Git 工作区（worktree）

多个 Agent 并行改同一个仓库时，各自的改动要分开。`spawn` 可以给 Agent 建一个独立的 git worktree，Agent 在自己的分支上工作，完成后再合并回来。命令、参数和 `--json` 字段与 ClawTeam 的 `workspace` 一致；git 操作都在 daemon 所在的机器上执行，所以 `--repo` 写的是那台机器上的路径。

## spawn 何时建 worktree

由 daemon 的工作区模式决定：环境变量 `TEAM_WORKER_WORKSPACE`，或 `~/.team-worker/config.json` 的 `workspace`。

| 模式 | 行为 |
|---|---|
| `auto`（默认） | 知道仓库时建 worktree：用 `--repo` 指定，或本地执行 `spawn` 时当前目录在某个 git 仓库里。都没有就用普通工作目录 |
| `always` | 必须建 worktree，找不到仓库就报错 |
| `never` | 不建 worktree |

- `--workspace`（`-w`）和 `--no-workspace` 覆盖模式。`--workspace` 时找不到仓库会报错，不会悄悄退回普通目录。
- `--no-workspace --repo <路径>` 让 Agent 直接在仓库里工作，不隔离。
- 显式 `--cwd` 时按原样使用该目录，不建 worktree；`--cwd` 不能和 `--repo` 或 `--workspace` 一起用。
- 远程执行（`--remote` 或 `TEAM_WORKER_SERVER_URL`）时客户端的当前目录对 daemon 没有意义，只认 `--repo`。
- `launch -w` 给模板里每个 Agent 建 worktree，`launch --repo` 同理；不加时 `launch` 不建。

worktree 的约定：

- 分支 `clawteam/<team>/<agent>`，从仓库当前检出的分支（detached 时为提交号）拉出，这个分支记为基准分支。`--repo` 或当前目录在另一个 worktree 里（比如 leader 自己的 worktree）时，按它所属的主仓库算，基准取主仓库检出的分支。
- 目录是 Agent 的工作目录 `<数据目录>/workspaces/<team>/<agent>`。
- 同一个 Agent 再次 spawn 时，已登记的 worktree 在原位置复用（包括从 ClawTeam 导入的），里面未提交的改动保留；worktree 删掉后分支还在时，新 worktree 接着用这个分支，之前的提交都在。
- 目录已存在且不是这个 Agent 的 worktree 时报错，不会删除或接管它。
- spawn 失败时撤销本次新建的 worktree 和分支，复用的不受影响。
- 第一条提示词里写明 worktree、分支和基准分支，并要求完成任务前提交。

## 团队的默认仓库

不想每次 spawn 都写 `--repo` 时，给团队设默认仓库：

```bash
team-worker team spawn-team demo --repo /srv/app     # 建团队时设置
team-worker team repos demo /srv/app /srv/web        # 替换
team-worker team repos demo                          # 查看
team-worker team repos demo --clear                  # 清除
```

spawn 取仓库的顺序：`--repo`，团队的默认仓库，最后才是本地执行时当前目录所在的仓库。`--no-workspace` 和 `never` 模式下不使用默认仓库。模板的 `repos` 会成为 `launch` 建出的团队的默认仓库（见 [templates.md](templates.md)）。

## 多个仓库

`--repo` 可以重复。只有一个仓库时，Agent 的工作目录本身就是 worktree（与 ClawTeam 相同）；多个仓库时工作目录是普通目录，每个仓库一个以仓库目录名命名的 worktree 子目录（重名时加 `-2`、`-3`），各仓库的分支同名：

```text
workspaces/demo/alice/
├── server/   # server 仓库的 worktree，分支 clawteam/demo/alice
└── web/      # web 仓库的 worktree，分支 clawteam/demo/alice
```

下面的命令默认作用于 Agent 的全部仓库，`--repo` 可以只处理其中一个（写仓库路径或目录名）。

## 命令

```bash
team-worker workspace list demo                    # 团队的全部 worktree
team-worker workspace status demo alice            # 未提交改动的 diff 统计
team-worker workspace checkpoint demo alice -m "wip"
team-worker workspace merge demo alice             # 合并回基准分支，--target 换目标，--no-cleanup 保留 worktree
team-worker workspace cleanup demo -a alice        # 不带 -a 时清理整个团队
team-worker workspace import demo                  # 接管从 ClawTeam 迁来的团队的 worktree
```

- `import`：读取 daemon 机器上 ClawTeam 的 `~/.clawteam/workspaces/<team>/workspace-registry.json`（`--file` 可换），登记其中仍然存在的 worktree，已经不在的跳过。之后这些成员再次 spawn 时，就在原来的 worktree 里继续工作。

- `checkpoint`：`git add -A` 后提交，没有改动时返回 `no_changes`。默认提交信息 `[clawteam] checkpoint: <agent> @ <时间>`。
- `merge`：先 checkpoint，再把每个仓库用 `git merge-tree` 试合并（不碰工作区，需要 git 2.38 及以上）。任何一个仓库有冲突，或目标分支检出处有未提交改动，就一个都不合并，返回 `merge_failed` 并列出原因，退出码为 1。全部没问题才逐个合并，都生成合并提交。合并不切换任何分支：目标分支检出在某个 worktree 时在那里合并，没检出时直接移动分支引用。合并后默认删除 worktree 和分支。
- `cleanup`：先 checkpoint，再删 worktree；分支已经合并进基准分支时一起删掉，否则保留，避免丢掉提交（ClawTeam 会直接删）。
- `team cleanup` 删除团队时同样处理：worktree 删掉，有未合并提交的分支保留。

团队通过 PR 合并代码时（比如由用户在代码平台上审核），不用 `workspace merge`，让 Agent 推送自己的分支即可。

## 跨 Agent 的改动上下文：`context`

依赖上面的 worktree，看各成员的工作都改了什么。算法和 `--json` 字段与 ClawTeam 的 `context` 一致：

```bash
team-worker context diff demo alice        # alice 相对基准分支的改动：文件、增删行数、提交数
team-worker context files demo             # 每个文件被哪些成员改过
team-worker context conflicts demo         # 被多个成员改过的文件：改动行有交集为 high，否则 medium
team-worker context log demo -n 20         # 各成员分支上的提交，按时间倒序
team-worker context inject demo alice      # 给 alice 的上下文：重叠文件、别人在这些文件上的提交、她的任务所等待的上游成员的改动
team-worker context inject demo alice --send   # 同时作为消息发给 alice
```

- 只算成员自己提交的改动：比较的是成员 worktree 当前检出的提交和基准分支的分叉点之后，再去掉它检出而不是自己提交的部分（按 worktree 的 HEAD reflog 区分，成员共用同一个 git 身份时也成立）。所以 reviewer、tester 把 worktree 重置到 PR 分支审查或验证，不会被算成改了同样的文件，也不会报冲突；他们在上面追加的提交仍算自己的。未提交的改动不计入，`context diff` 会另报工作区里未提交的文件数（JSON 字段 `uncommitted_files`）。成员在 worktree 里切到了别的分支时，按它实际所在的提交算（ClawTeam 按登记的分支名算）；worktree 已删除时按分支算。基准分支落后于它的上游（比如主仓库从不 pull、本地 master 落后于 origin/master）时，按上游比较，否则上游合进来的提交会被算成每个成员的改动。
- 涉及多个仓库时，文件写成 `<仓库目录名>/<路径>`，只有同一个仓库里的同一文件才算重叠；`--repo` 只看一个仓库。
- spawn 时，有 worktree 的成员会把 `inject` 的内容放进第一条提示词的 `## Context`（没有内容时不放）。ClawTeam 也是这样做的，但它只在 spawn 时用；`--send` 可以随时补发。

## 与 ClawTeam 的不同

- 记录存在 SQLite 的 `workspaces` 表，按 Agent 和仓库各一行；ClawTeam 用每个团队一个 JSON 文件，并发 spawn 时可能丢记录。
- 支持一个 Agent 关联多个仓库。
- merge 先整体试合并，不切换主仓库的分支；cleanup 和删团队时保留未合并的分支；再次 spawn 时复用已有 worktree，不会删掉重建。
- 显式 `--workspace` 但找不到仓库时报错（ClawTeam 会悄悄不建）。
- `context` 按 worktree 实际检出的提交计算，并且只算成员自己提交的部分，审查时检出别人的分支不算冲突；`log` 用控制字符分隔字段，提交信息里有 `|` 也能正确解析；`inject` 可以用 `--send` 直接发给成员。

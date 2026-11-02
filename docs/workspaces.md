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

## 与 ClawTeam 的不同

- 记录存在 SQLite 的 `workspaces` 表，按 Agent 和仓库各一行；ClawTeam 用每个团队一个 JSON 文件，并发 spawn 时可能丢记录。
- 支持一个 Agent 关联多个仓库。
- merge 先整体试合并，不切换主仓库的分支；cleanup 和删团队时保留未合并的分支；再次 spawn 时复用已有 worktree，不会删掉重建。
- 显式 `--workspace` 但找不到仓库时报错（ClawTeam 会悄悄不建）。

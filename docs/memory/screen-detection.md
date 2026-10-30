# Agent 画面状态判断
> 从 tmux 画面和终端标题判断 Agent 空闲 / 工作 / 等用户时踩过的坑与实测结论。

- 2026-10-30：旧的 `InputPromptVisible` 只认 `>`/`❯`/`›` 提示符，把 Claude 回复末尾的编号列表误判成菜单、又认不出各 CLI 自己的菜单；改为按 CLI 加载 herdr 的检测清单（`internal/agent/builtin/detect/*.toml`，取自 herdr 9c38bbc64e3b，之后上游 15 个提交未改清单和 `manifest.rs`）。daemon 只在命中规则带 `visible_idle` 时打字，规则都不命中的「idle 兜底」不算数，因为没见过的菜单也是这个结果。
- 2026-10-30：`tmux display-message -p '#{pane_title}'` 在 `LANG=C` 等非 UTF-8 环境下把非 ASCII 字符输出成 `_`，Claude 的 `✳`/盲文转圈全丢；加 `tmux -u` 解决（`internal/tmux/tmux.go` 的 `Title`）。`capture-pane -p` 不受影响。
- 2026-10-30：Claude Code 2.1.302 在 tmux 里只发 OSC 0 标题（空闲 `✳ `，工作时 U+2800–28FF 盲文或 `◐◑◒◓`）和一次 OSC 7501 查询 `?`，不发 OSC 9 进度。按规范回 `OSC 7501 ; ? : states=…:kinds=… ST` 的 5 种写法在 pty 里实测都没让它开始上报状态，原因待验证（需要看 Claude 源码）；目前只用标题 + 画面。
- 2026-10-30：herdr 清单是 Rust 正则，`\uXXXX`、`\u{X}` 在 Go RE2 里不合法，加载时改写成 `\x{X}`（`internal/agent/detect.go` 的 `goRegex`）；其余语法实测 7 份清单都能直接编译。
- 2026-10-30：Codex 的批准框画在最后一行 `›` 提示符**下方**，`after_last_prompt_marker` 区域才看得到；测试样例放在提示符上方会误判为空闲。
- 2026-10-30：本机 Claude Code 2.1.302 在 tmux 里实测（`LANG=C`，capture-pane + `tmux -u` 标题）：信任提示 → `blocked`（`live_blocked_form`），思考中 `✢ Julienning… (3s · thinking)` → `working`（`live_turn_working`），结束后 → `idle`（`live_prompt_box`，`visible_idle`）。这一版底栏没有 `esc to interrupt`，标题在工作时也一直停在 `✳`（115 上见过 `◐`，差别原因待验证），靠的是转圈那一行。流式输出正文时转圈行有时不显示、会被判成 idle，但此时画面一直在变，nudger 的「8 秒不变」门槛会挡住。

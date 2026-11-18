# 飞书助手回复排查
> 从 Codex 会话日志定位飞书答复正文丢失。

- 2026-11-18：飞书发出「这次没有收到完整的答复」时，发送任务仍可能显示 `sent`，因为那是通用报错的投递状态。成功后队列会清空正文。从专用 Codex 目录的 `sessions/` 检查同一会话的路由和问答两轮。实测问答续接后仍可能返回 `action=answer`，正文在 `description` 中而 `summary` 为空。回归用例见 `internal/chat/codex_test.go`。

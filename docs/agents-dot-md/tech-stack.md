# 技术栈与实现规范

> 技术选型、数据访问与配置约定、构建与验证命令；写代码前先看本文，按既有方式扩展。

本文回答「这个项目用了什么、新代码该照着什么写、改完怎么验证」。**系统结构（模块边界、分层、关键链路、设计决策）在 `architecture.md`**，两边别重复。

## 一、项目概览

- 定位：用 Go 重写的 [ClawTeam](https://github.com/HKUDS/ClawTeam) 多 Agent 协作工具，单机使用，给 Agent 和人用的团队 / 任务 / 收件箱 CLI。
- 仓库形态：单 Go module `github.com/benlocal/team-worker`，两个入口 `cmd/team-worker`、`cmd/team-workerd`；`skills/team-worker` 是随仓库分发的 agent skill。
- 主要语言与版本：Go 1.26（`go.mod` 中 `go 1.26.0`）。
- 结构总览：见 `architecture.md`。

## 二、核心技术栈

| 类别 | 选型 | 版本 | 备注 |
|---|---|---|---|
| 语言 / 运行时 | Go | 1.26 | |
| CLI 框架 | `github.com/spf13/cobra` | v1.10.2 | |
| 模板解析 | `github.com/BurntSushi/toml` | v1.6.0 | 仅 `internal/templates` |
| agent 运行环境 | `tmux`（运行时外部命令） | — | 经 `internal/tmux` 调用；没有 tmux 时用 subprocess 后端 |
| HTTP | 标准库 `net/http`（Go 1.22+ 方法 / 路径模式路由） | — | 不引入第三方路由 |
| 数据库驱动 | `modernc.org/sqlite`（纯 Go，免 cgo） | v1.60.1 | 通过 `database/sql` 使用 |
| 数据库 | SQLite，WAL 模式 | — | |
| 日志 | 标准库 `log/slog` | — | 仅服务端 |
| Web 看板 | 标准库 `net/http` + `embed` | — | `internal/board`，静态页面打进二进制 |
| 构建工具 | `make` + `go` | — | |

> 只列**实际在用**的；同类能力已有选型时不再引入第二套（见 `code-checklist.md`）。

## 三、数据访问规范

- 查询方式：`database/sql` 手写 SQL，不用 ORM；参数一律用 `?` 占位。
- 连接：DSN 里设 `busy_timeout(5000)`、`journal_mode(WAL)`、`foreign_keys(1)`、`synchronous(NORMAL)`；`SetMaxOpenConns(1)`。
- 事务：写操作走 `Store.write`，提交成功后自动触发 `OnChange`；事务内复用的辅助函数接收 `queryer`。
- 主键：团队、成员按名字；任务 ID 由 `randomHex` 生成；消息 / 事件用自增 ID。
- 列表类字段（`blocks`、`metadata`、`command`）以 JSON 文本存储。
- 迁移：在 `store.migrations` 末尾追加，不改已有条目。
- 错误：业务错误用 `invalidf` / `notFoundf` / `conflictf`，`server.respond` 据此映射 HTTP 状态码。

## 四、配置管理

- 只有环境变量 `TEAM_WORKER_*`（见 README《配置》）和可选的 `~/.team-worker/config.json`（`data_dir`、`user`），读取逻辑集中在 `internal/config`。
- 新配置项加到 `internal/config`，并同步 README 的配置表。

## 五、构建与验证命令

改完代码至少跑一遍：

```bash
make build      # go build -o bin/ ./cmd/...
make test       # go test ./...
make vet        # go vet ./...
make fmt        # gofmt -w .
go test -race ./...   # 改了 Hub、长轮询、daemon 生命周期等并发路径时补跑
```

测试：`internal/store/store_test.go` 直接测 store；`internal/cli/cli_test.go` 用 `httptest` 起真实 server，端到端跑 CLI 命令；`internal/spawn`、`internal/templates` 各有单元测试。

仓库没有 `.golangci.yml`。用默认配置跑 `golangci-lint run` 会报一批 `defer Close` / `tx.Rollback` 之类的 errcheck，这些不算项目的检查门槛，以 `make vet` + `make test` 为准。

## 六、部署方式

本机使用，无服务端部署：`make install` 把两个二进制装到 `GOBIN`（未设置时为 `GOPATH/bin`）。CLI 首次调用会自动拉起 `team-workerd`。

## 七、其他约定

- 代码注释、doc comment、日志与错误信息一律用英文（见 `translation.md`）。
- CLI 输出与 `--json` 字段名对齐 ClawTeam；行为差异写进 README《与 ClawTeam 的差异》。
- 新增或改动 API 时同步 README 的 API 表。

## 八、快速参考：新增一个 API + CLI 命令要动哪些文件

以 `task stats` 为例：

1. `internal/store/tasks.go` — 加导出方法 `TaskStats`，必要时在 `internal/model` 加返回类型。
2. `internal/server/server.go` — `routes` 注册 `GET /v1/teams/{team}/tasks/stats`，handler 调 store 后 `respond`。
3. `internal/client/client.go` — 加 `TaskStats` 方法。
4. `internal/cli/task.go` — 加 `taskStatsCmd` 并挂到 `taskCmd`，用 `a.emit` 同时支持表格和 `--json`。
5. 验证：在 `store_test.go` / `cli_test.go` 补用例，跑 `make test`；README 的命令和 API 表同步更新。

> 挂载点与禁止的改法见 `architecture.md` 的《扩展点》。

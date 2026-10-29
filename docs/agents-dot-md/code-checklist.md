# 代码 checklist（强制自检）

> 写完 / 改完代码必须逐条对照的硬性自检清单；新增条目直接追加本文件。

本文件由 `AGENTS.md`（即 `CLAUDE.md`）拆出，收敛「写代码时必须逐条自检」的硬性条目。顶层只留一条强制指针，条目细节都在这里；新增条目直接追加到本文件，不要写回 `AGENTS.md`。

**通用条目**是跨语言的硬红线；**语言专项**由 agents-dot-md skill 的 `scaffold.py` 按本仓库实际语言注入，是该语言公认的固定规范。落地时请按本项目实际情况增删改——不适用的直接删掉，别留着占位；踩过的坑按下面的格式追加。

## 怎么写一条 checklist

每条包含四部分，前三部分缺一不可——只写规则不给例子，下一个 agent 照样会踩：

1. **规则**：一句话说清「必须怎样 / 禁止怎样」。
2. **反例**：真实踩过的写法，标明后果（报什么错、线上出过什么事故）。
3. **正例**：照抄就对的写法。
4. **强制**：由什么自动拦下，写成 `强制：<lint 规则 / 测试名 / 脚本命令>`；只能靠人判断的写 `强制：无（靠自检）`。能加检查就优先加检查：文字规则 agent 跳过了也不会报错。`强制：无` 的条目反复被违反，就该升级成检查。

条目来源优先级：**线上事故 > 评审反复提的意见 > 团队口头约定**。没踩过的坑不必预防性地写进来，这里只放硬性红线；软性建议归 `coding-guidelines.md`。

---

# 通用条目（跨语言）

## 1. SQL 用 `?` 占位，迁移只追加不修改

数据库是 `modernc.org/sqlite` 驱动的 SQLite（WAL）。SQL 参数一律用 `?` 占位，不拼接用户输入；表结构变更在 `internal/store/migrations/` 追加递增版本号的 Goose SQL 文件，用 `go:embed` 内置。Goose 记录迁移进度，各 SQL 的 Up 部分同步设置对应的 `PRAGMA user_version`。

- 反例：修改已有的迁移字符串。已经建过库的数据目录不会再执行它，新旧库结构从此不一致。
- 正例：追加包含 `ALTER TABLE ...` / `CREATE TABLE ...` 和对应 `PRAGMA user_version` 的 Goose SQL 文件，并在 store 测试中覆盖旧库升级和新字段。
- 强制：无（靠自检）

## 2. 禁止 1+N：循环里不查库、不调远程接口

循环体内逐条查询 / RPC / HTTP 调用，量一上来就是必然的超时事故。

- 反例：逐个 id 查一次库，`N` 条数据打 `N` 次请求。
- 正例：一次批量查询后在内存里按 key 分组；远程调用同理，优先批量接口。

## 3. CLI 输出、`--json` 字段与 API 契约不得随意改动

`--json` 的字段名与 ClawTeam 一致（`internal/model` 的 JSON tag），agent prompt 和脚本依赖它；API 出错统一返回 `{"error": "..."}`，状态码由 `server.respond` 按 `store.Error` 的 Kind 映射（400 / 404 / 409，服务关闭时 503）。API 没有鉴权，访问控制只靠 socket 文件权限 0600。

- 改字段名、命令结构或错误码语义前，先对照 ClawTeam 的行为；确需不同的，写进 README《与 ClawTeam 的差异》。
- 新增或改动 API、命令、配置项时，同步更新 README 对应的表。
- 强制：`internal/cli/cli_test.go` 的端到端用例（`make test`）

## 4. 敏感信息不入库

密钥、密码、token、内网凭据一律不写进受版本控制的文件（含配置文件、测试用例、注释、提交信息）。走环境变量或配置中心；本地开发凭据放未入库文件（见 `environment.md`）。

提交前自检：`git diff` 里有没有出现形如密码 / `AK` / `SK` / 私钥的字面量。

## 5. 沿用既有技术选型，不引入第二套同类方案

新增三方依赖前先检查：根构建文件是否已有同类能力、能否复用项目内的公共封装。无充分理由不得并存两套 HTTP 客户端 / ORM / JSON 库 / 日志门面。

分层、命名、依赖注入方式跟随所在模块既有风格（详见 `tech-stack.md`），不做跨模块风格漂移。

## 6. 代码注释、日志与错误信息使用英文

源码里的注释（doc comment、行内注释、TODO）、日志、错误信息和 CLI 帮助文本一律用英文，与现有代码一致。文档（README、`docs/`、`skills/`）仍用中文。

- 反例：`// 释放任务锁`、`return invalidf("任务不存在")`
- 正例：`// releaseTasks returns an exited agent's in-progress tasks to pending.`、`return notFoundf("Task '%s' not found", id)`
- 强制：无（靠自检；提交前 `git diff` 里搜一遍中文字符）

---

# Go 专项

## G1. 每个 error 都要处理，并保留错误链

返回的 `error` 要么就地处理，要么向上返回；包装错误使用 `%w`，让调用方可以用 `errors.Is` / `errors.As` 判断根因。不要用字符串匹配错误，也不要用 `_` 丢掉有意义的错误。

- 反例：`data, _ := io.ReadAll(r)`、`return fmt.Errorf("read failed: %v", err)`
- 正例：`return fmt.Errorf("read config %s: %w", path, err)`

## G2. Context 沿调用链传递，创建后及时 cancel

会阻塞、访问网络或数据库的函数把 `context.Context` 作为第一个参数，并把上游 context 继续传下去。`context.WithCancel` / `WithTimeout` 返回的 `cancel` 必须及时 `defer cancel()`，避免定时器和子任务泄漏。

- 反例：下层函数重新用 `context.Background()`，或创建 timeout 后丢掉 `cancel`。
- 正例：`func Load(ctx context.Context, id string) error`；派生超时时紧接着写 `defer cancel()`。

## G3. 每个 goroutine 都必须有明确的退出路径

启动 goroutine 前写清谁关闭输入、谁取消 context、谁等待退出。禁止只启动不回收；共享状态使用 channel、mutex 或原子操作保护，并用 `go test -race ./...` 检查关键并发路径。

- 反例：后台 `go func() { for { work() } }()`，没有取消条件。
- 正例：循环同时监听工作 channel 与 `ctx.Done()`，调用方用 `WaitGroup` 等待退出。

## G4. I/O 资源必须关闭，HTTP 响应先检查状态

文件、HTTP response body、数据库 rows 等资源在成功创建后立即安排关闭，并检查 `Close` / `rows.Err()` 等延迟错误。HTTP 客户端设置超时，读取 body 前先处理非 2xx 状态。

- 反例：`client.Get(...)` 后直接读取 body，不设超时、不关闭响应体。
- 正例：检查请求错误后立即 `defer resp.Body.Close()`，再校验状态码并读取受限大小的响应体。

## G5. 公共 API 避免暴露可变内部状态

slice、map、指针传递的是共享数据；返回或保存后可能被外部修改时要复制。并发读写 map 必须同步，结构体中包含 mutex 时禁止按值复制。

- 反例：getter 直接返回结构体内部的 map，调用方可以绕过锁修改它。
- 正例：锁内复制 map / slice 后返回副本；含 mutex 的结构体只传指针。

## G6. 接口由使用方定义，保持小而具体

接口放在消费它的包中，只声明调用方真正需要的方法；函数能接收具体类型时不预先抽象。返回具体类型，让调用方按需要定义接口。

- 反例：生产者包预先定义包含十几个方法的 `Service` 接口，所有调用方被迫依赖它。
- 正例：消费方按实际调用定义 `type UserFinder interface { Find(context.Context, string) (User, error) }`。

## G7. 提交前统一格式化并跑静态检查

改动过的 Go 文件必须经过 `gofmt`；运行 `make fmt`、`make test`、`make vet`，并对并发相关改动补跑 `go test -race ./...`。生成代码按项目命令重新生成，不手改生成产物。

- 反例：只运行单个包的测试就提交跨包改动，或手改 `*.pb.go`。
- 正例：先跑项目生成命令，再运行 `gofmt`、全量测试与项目约定的静态检查。

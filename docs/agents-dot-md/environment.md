# 开发环境 / 推送 / 发布

> 本地开发数据目录隔离、Git 推送、安装发布。

## 开发环境信息
> ⚠️ 本项目没有服务器或账号凭据。如果以后需要，放在本地未入库文件（如 `dev-env.local.md`，加入 `.gitignore`），不写进任何受版本控制的文件。

- 手工调试时用独立数据目录，别动 `~/.team-worker` 里的真实数据：`TEAM_WORKER_DATA_DIR=$(mktemp -d) bin/team-worker ...`。
- 数据目录路径较长时 socket 路径会超过约 100 字符的上限，用 `TEAM_WORKER_SOCKET` 把 socket 放到短路径下。
- 调试完记得停掉自动拉起的 `team-workerd`：先用 `pgrep -af team-workerd` 确认 PID，再 `kill <pid>`。

## Git 推送 / 拉取
主分支为 `main`。目前仓库还没有配置远端。

## 发布 / 部署
本机安装：`make install`。没有发布流水线。

## 数据库升级与恢复

`team-workerd` 启动时通过 Goose 执行内置的 `internal/store/migrations/*.sql`。SQL 文件由 `go:embed` 编入程序，无需另外分发。每个迁移、Goose 版本记录和 `PRAGMA user_version` 更新在同一事务内完成。已发布的 SQL 文件只追加，不改写。新建数据库直接初始化；已有版本的数据库升级前，会在数据库同目录创建 `team-worker.db.before-v<旧版本>-<随机值>.db` 备份，权限为 0600。

只有 `user_version` 的旧库会先备份，再把已执行的版本登记到 `goose_db_version`，不会重放历史建表语句。已接入 Goose 的库会校验两处版本是否一致，不一致时停止启动。

新增迁移时，在 `internal/store/migrations/` 添加下一版本的文件，例如 `00005_example.sql`，沿用已有文件的 `-- +goose Up` 标记，并在末尾设置 `PRAGMA user_version = 5;`。随后运行 `make fmt && make vet && make test`，再重新构建二进制。恢复旧版使用备份，不提供自动降级迁移。

备份使用 SQLite `VACUUM INTO`，包含 WAL 中已提交的数据，可独立打开。备份失败时不执行迁移。失败的迁移会回滚，之前已成功的迁移保留；修正失败原因后重新启动会从当前版本继续执行。迁移错误会附上升级前备份路径。数据库版本高于程序支持版本时，启动失败，不尝试降级。

恢复升级前的数据时：

1. 停止使用该数据目录的 `team-workerd`，并停止会调用 CLI 的 Agent，避免 CLI 自动拉起服务。
2. 将当前 `team-worker.db` 和仍存在的 `team-worker.db-wal`、`team-worker.db-shm` 一并移到另一个目录保留，不混用新旧文件。
3. 将目标备份复制为该数据目录的 `team-worker.db`，保持权限 0600，原备份保留。
4. 若要回退程序，使用支持该备份版本的二进制启动；使用新程序启动会再次执行升级。

恢复会回到备份时刻，备份之后写入的数据不会出现在恢复后的数据库中。备份不会自动删除，确认升级正常后自行清理旧备份。

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

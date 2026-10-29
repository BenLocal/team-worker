# OpenBao 界面使用指南

team-worker Docker 默认安装并启动 OpenBao（`TEAM_WORKER_OPENBAO=1`）；设为 `0` 可关闭启动。下文的 `<主机>:<端口>` 指容器的发布地址，例如 `http://<主机>:28088`；界面说明基于 OpenBao 2.7.2。

## 概述

OpenBao 在容器内只监听 `127.0.0.1:8200`，Nginx 在发布端口上代理界面 `/ui/` 和 API `/v1/`。启动时 `openbao-agent.service` 把 `magpie-config/openbao.yaml` 中的密钥导入 KV v2 挂载 `secret`，再由 OpenBao Agent 渲染成成员使用的 0600 文件。

默认 `TEAM_WORKER_OPENBAO_AUTO_UNSEAL=1`：首次启动自动初始化，解封密钥和 root 令牌保存在容器 home 卷的 `/home/node/.openbao/init.json`（权限 0600），之后每次启动自动解封，再导入 `openbao.yaml` 并渲染文件。设为 `0` 时初始化和解封需要人工完成，见 README 的“OpenBao 密钥服务”。

## 打开界面并登录

1. 浏览器打开 `http://<主机>:<端口>/openbao`，会跳转到 `/ui/`。
2. 未登录时进入登录页 **Sign in to OpenBao**。地址形如 `/ui/vault/auth?redirect_to=%2Fvault%2Funseal&with=token`：
   - `vault` 是 OpenBao 从 Vault 继承的界面路由名，与 Vault 产品无关；
   - `with=token` 表示预选 Token 登录方式；
   - `redirect_to` 是登录后要去的页面。实例已经解封时，登录后直接进入密钥引擎列表。
3. 在方式标签中选择 **Token**，**Namespace** 留空，在 **Token** 中填写令牌，点击 **Sign In**。

自动初始化的部署，root 令牌在宿主机上这样读取（容器名按实际部署填写）：

```bash
docker exec -u node <容器名> python3 -c 'import json;print(json.load(open("/home/node/.openbao/init.json"))["root_token"])'
```

人工初始化的部署，使用初始化时保存的 root 令牌，或管理员发放的令牌。令牌不要发到聊天、任务或代码中。

## 查看密钥

登录后的导航：

| 菜单 | 用途 |
| --- | --- |
| **Secrets engines** | 密钥引擎列表，导入的密钥在 `secret/` 下 |
| **Access** | 认证方式与身份，可看到导入程序创建的 `approle` |
| **Policies** | 访问策略，`team-worker-agent` 只读策略在这里 |
| **Tools** | 随机数、哈希等辅助工具 |
| **Raft Storage** | 存储节点与快照 |

查看步骤：

1. 在 **Secrets Engines** 中点击 `secret/`（标注 **Version 2**，即 KV v2）。
2. 进入 `openbao.yaml` 中声明的目录，例如 `team-worker/`，再点击具体路径，例如 `git` 或 `vikunja`。
3. 详情页的 **Secret** 标签列出各个键，值默认遮挡；点击眼睛图标显示，点击 **Copy** 复制。**Metadata** 标签显示版本和时间。
4. 页面上的版本选择（如 **Version 1**）可查看历史版本。导入程序只在内容变化时写入新版本。

详情页还有 **Create new version**、**Download** 和 **Delete**。修改密钥请按下一节操作，不要在界面中删除导入的路径。

## 修改密钥

`magpie-config/openbao.yaml` 是密钥的来源。在界面中用 **Create new version** 修改的值，会在下次启动导入时被 YAML 中的值覆盖。正确做法：

1. 编辑宿主机上的 `magpie-config/openbao.yaml`，修改 `secrets` 中对应路径的值；新增路径时同时在 `files` 的模板中引用它。
2. 重启容器，例如 `docker compose up -d --force-recreate`（宿主机可能使用 `docker-compose`）。导入程序启动后重新导入并授权，Agent 重新渲染文件。
3. 在界面中确认新版本已经写入，在容器内确认渲染文件已更新（只检查修改时间，不打印内容）：

   ```bash
   docker exec -u node <容器名> ls -l /home/node/.config/team-worker/
   ```

YAML 中删除的路径不会从 OpenBao 中删除，但会从 `team-worker-agent` 只读策略中移除，成员无法再读取。

## 成员如何使用密钥

成员不直接访问界面，而是使用 OpenBao Agent 渲染的文件，路径由 `openbao.yaml` 的 `files` 决定，常见的有：

- `secrets.env`：以 `export KEY='value'` 形式提供只认环境变量的工具所需的令牌。需要时在同一条命令中加载：

  ```bash
  set -a; . ~/.config/team-worker/secrets.env; set +a; <命令>
  ```

- Git 凭据文件：由部署的 Git 配置以 `helper = store --file <文件>` 引用，`git push`、`git fetch` 无需额外操作。

成员不要用 `cat`、`echo`、`env` 打印密钥，也不要写入任务板、消息、PR 或仓库。

## 安全与运维

- **root 令牌**：权限最高，只用于管理。用完点击右上角用户菜单（显示 `root`）退出登录；在共用电脑上不要保存。
- **Seal OpenBao**：导航中的这个按钮会立即封存实例，成员无法再刷新渲染文件，已渲染的文件不受影响。自动解封的部署需重启容器才能再次解封，非必要不要点击。
- **备份**：把 `init.json` 另行备份到离线位置，并用 **Raft Storage** 或 `bao operator raft snapshot save` 保存数据快照。只有数据而没有解封密钥无法恢复。
- **自动解封的风险**：解封密钥与数据在同一个 home 卷中，取得该卷即可解开全部密钥，需限制宿主机和卷的访问。
- **日常查看用低权限账号**：可在容器内用 root 令牌开启用户名密码登录，并授予只读策略，之后在登录页选择 **Username** 方式：

  ```bash
  docker exec -it -u node <容器名> bash
  export BAO_ADDR=http://127.0.0.1:8200
  bao login            # 输入 root 令牌
  bao auth enable userpass
  bao policy write viewer - <<'EOF'
  path "secret/data/*"     { capabilities = ["read", "list"] }
  path "secret/metadata/*" { capabilities = ["read", "list"] }
  EOF
  bao write auth/userpass/users/<用户名> policies=viewer password=<密码>
  ```

## 常见问题

| 现象 | 原因与处理 |
| --- | --- |
| `/openbao`、`/ui/` 返回 502 | 未设置 `TEAM_WORKER_OPENBAO=1`，或 OpenBao 服务未运行。systemd 模式用 `systemctl status openbao` 检查 |
| 登录后显示 Sealed，或要求输入解封密钥 | 实例已封存。自动解封的部署重启容器即可；人工模式按门槛输入解封密钥 |
| 登录提示 `Authentication failed: namespace not found` | **Namespace** 输入框不是空的，常见于令牌粘贴到了 Namespace 框，或浏览器自动填充。清空 Namespace，只在 **Token** 框中填令牌；仍被自动填充时改用无痕窗口 |
| 登录页提示令牌无效 | 令牌错误或已过期。root 令牌不会过期，其他令牌按其 TTL 失效 |
| 打开某个路径显示 permission denied | 当前令牌没有该路径的权限，换用有权限的令牌或调整策略 |
| `secret/` 下看不到预期的路径 | 导入尚未完成或失败。systemd 模式查看 `journalctl -u openbao-agent`，确认出现“已导入 N 个密钥路径” |

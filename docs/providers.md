# providers.yaml：模型供应商与路由

> Docker 镜像里给 Magpie 配置模型供应商、路由分组和各 Agent 默认模型的文件：字段、导入时机、常见做法和注意事项。

`providers.yaml` 告诉容器里的 Magpie：有哪些模型供应商（地址、Key、可用模型）、哪些模型组成一个自动切换的分组，以及 Claude Code、Codex、dsh 默认用哪个模型。容器每次启动时，入口脚本执行 `team-worker-ops magpie <文件>` 把它导入 Magpie；成员的请求都经 Magpie 网关转发给这些供应商。

## 文件放在哪

| 启动方式 | 位置 |
| --- | --- |
| `./docker/team-worker.sh start` | 配置目录里的 `providers.yaml`（默认 `docker/magpie-config/`，`TEAM_WORKER_CONFIG_DIR` 可改），或 `TEAM_WORKER_PROVIDERS_FILE` 指定的宿主机文件 |
| Compose | 把配置目录只读挂载到 `/etc/team-worker`，容器内读 `/etc/team-worker/providers.yaml` |
| 容器内 | `TEAM_WORKER_PROVIDERS_FILE`，默认 `/etc/team-worker/providers.yaml` |

文件里通常有 API Key，放在部署主机的配置目录里，不要提交到仓库；需要从环境变量或 OpenBao 渲染的文件取 Key 时用 `key_env`（见下文）。

## 一个完整的例子

```yaml
providers:
  # 主力供应商：Anthropic 兼容接口
  - id: main
    api: anthropic
    url: https://api.example.com/anthropic
    key_env: MAIN_API_KEY
    models:
      - deepseek-v4.1-flash

  # 备用供应商：OpenAI 兼容接口，地址取自环境变量
  - id: backup
    api: openai
    url_env: BACKUP_API_BASE
    key: "sk-..."
    models:
      - qwen3.6-flash
      - deepseek-v4.1-flash

groups:
  # 优先 main；main 答不了（比如额度用完）才用 backup，main 恢复后自动回到 main
  - id: main-first
    models:
      - main/deepseek-v4.1-flash
      - backup/deepseek-v4.1-flash
    routing: order

claude:
  model: group/main-first
  effort: high

# 不写 codex 时，Codex 沿用 claude.model
codex:
  model: backup/qwen3.6-flash
  effort: medium
```

## 字段说明

顶层只允许 `providers`、`groups`、`claude`、`codex` 四项，写了别的字段整个文件校验失败。

### providers：供应商列表

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `id` | 是 | 供应商 ID，只能用小写字母、数字和连字符，文件内不能重复。Agent 选模型时写成 `<id>/<模型>` |
| `api` | 否 | 接口类型：`openai`（默认，OpenAI 兼容的 chat 接口）、`anthropic`、`responses`、`gemini`。每个供应商只能写一种 |
| `url` / `url_env` | 二选一 | 接口地址，或存放地址的环境变量名。必须是 `http(s)://` 地址，不能带用户名密码 |
| `key` / `key_env` | 最多一个 | API Key，或存放 Key 的环境变量名。都不写或 `key: ""` 表示不需要认证 |
| `key_required` | 否 | `true` 表示这个供应商必须有 Key；Key 为空时 Magpie 先存一个占位值，并让带 keepalive 的 Leader 等 Key 配好再启动（见下文） |
| `models` | 是 | 开放给 Agent 的模型列表，至少一个；模型名里不能有逗号 |

`url_env`、`key_env` 引用的环境变量必须在容器里存在（compose 的 `environment:`、env 文件，或入口脚本加载的变量），没有设置就校验失败。

### groups：路由分组

把几个模型组成一个分组，Agent 选 `group/<id>` 时由 Magpie 决定每次请求发给其中哪个模型。

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `id` | 是 | 分组 ID，规则同供应商 ID |
| `models` | 是 | 模型列表，写成 `<供应商 id>/<模型>`，按顺序排列；也可以引用别的分组 `group/<id>` |
| `routing` | 否 | 怎么挑模型，默认 `order`，可选值见下表 |
| `stays` | 否 | 会话是否固定在答过的那个账号或 Key 上，默认 `"off"`；可选 `auto`、`session`、`turn`、`off`、`strict`。YAML 里 `off` 要加引号 |

| `routing` | 含义 |
| --- | --- |
| `order` | 每次先用第一个，它答不了才用下一个；第一个恢复后又回到它。适合"主力 + 备用" |
| `smart` | Magpie 默认：在还有余量的订阅里，先用最快重置额度的那个 |
| `usage` | 先用剩余额度最多的账号或 Key |
| `rotate` | 轮流使用 |
| `weight` | 按权重分配（权重用 `magpie group set <id> weight=…` 设置） |
| `pace`、`free`、`fast` | 按节奏分摊、优先免费额度、优先最近响应最快的；细节见 `magpie group help` |

Magpie 还支持 `drain`、`cheap`、`expiring` 等路由方式，但 `team-worker-ops` 的校验不接受它们，写进文件会导致启动失败；需要时在 Magpie 里用 `magpie group set <id> routing=…` 设置。

### claude、codex：默认模型

| 字段 | 说明 |
| --- | --- |
| `claude.model` | Claude Code 的模型，`<供应商 id>/<模型>` 或 `group/<id>`。**dsh 也用这个模型** |
| `claude.effort` | 推理强度：`low`、`medium`、`high`、`xhigh`、`max` |
| `codex.model` | Codex 的模型；不写时沿用 `claude.model` |
| `codex.effort` | Codex 推理强度：`minimal`、`low`、`medium`、`high`、`xhigh` |

环境变量 `TEAM_WORKER_MODEL` 有值时优先于 `claude.model`（同样作用于 dsh，以及没写 `codex.model` 时的 Codex）。`claude.model` 和 `TEAM_WORKER_MODEL` 都为空时，各 Agent 保持 Magpie 里现有的模型。Pi 等其他 Agent 不由这个文件设置，用 `magpie <agent> <模型>` 单独设。

## 导入时做了什么

每次容器启动（包括重启），`team-worker-ops magpie` 依次：

1. **校验整个文件**。有任何错误就什么都不改，并让启动失败（容器会按重启策略重试）。为了不泄露 Key，格式错误只报"检查格式、模型和环境变量"，不回显文件内容。
2. **逐个配置供应商**：Magpie 里没有的用 `magpie provider add` 新建；已有的用 `magpie provider set` 更新**文件里写到的字段**（`api` 对应的地址、Key、模型列表），其他设置保持不变。
3. **逐个配置分组**：`magpie group add`，同名分组会被整体替换。
4. **设置模型**：Claude Code 设为 `claude.model`，dsh 跟它一致；Codex 设为 `codex.model` 或沿用前者。模型设置失败（比如供应商在 Magpie 面板里被停用）只打 warning，Agent 保持原来的模型，不会让容器反复重启。

改完文件后不必重启容器，在容器里手动执行一次即可生效（以 node 用户执行，否则写出的配置文件属主会变成 root）：

```bash
docker exec -u node <容器名> team-worker-ops magpie /etc/team-worker/providers.yaml
```

正在运行的 Agent 通常在启动时读取模型设置，改了模型后它们大概要在下次启动或续接时才换过去。

## 常见做法

**主力 + 备用自动切换**：把主力和备用的模型放进一个 `routing: order` 的分组，再把 `claude.model` 设为这个分组，见上面的例子。用 `magpie usage --csv --last 10m` 可以看每个请求实际由哪个供应商回答（`requested_model` 与 `provider` 列）。

**测试一个供应商**：

```bash
docker exec -u node <容器名> magpie provider test <id>     # 逐个接口发测试请求
docker exec -u node <容器名> magpie provider <id>          # 看地址、Key（打码）和模型
```

`provider test` 用 Magpie 自带的测试模型；那个模型不在你的账号里时会报 400，并不代表供应商不能用，可以改用 `claude -p "回复 OK" --model <id>/<模型>` 实际调用一次确认。

**删除供应商**：从文件里删掉不会删除 Magpie 里已有的供应商，还要执行 `magpie provider rm <id>`。删之前确认没有 Agent 或分组在用它的模型（`magpie agents`、`magpie group <id>`）。

**一个供应商同时提供多种接口**：文件里只能写一种接口地址。其他地址（比如同时要 OpenAI 兼容和 Anthropic 接口）在 Magpie 里补设，例如 `magpie provider set <id> url=<OpenAI 兼容地址>`；之后导入只更新文件里写到的那一种，补设的地址会保留。但如果在 Magpie 里删掉这个供应商再重新导入，需要重新补设。

**Key 还没配好就启动**：给供应商写 `key_required: true` 并留空 Key。启动时 Magpie 先存一个占位 Key，并写出 `/tmp/team-worker-provider-pending`；带 keepalive 的 Agent 会一直等到这个文件消失才启动。是否等待按文件里的 Key 判断：把真实 Key 写进文件（或设好 `key_env` 指向的环境变量）后重新导入，没有缺 Key 的供应商时这个文件就会被删掉；只在 Magpie 面板里改 Key 不会解除等待。

## 注意事项

- 文件里的 Key 是明文。整个配置目录只给部署主机上需要的人读，不要贴到聊天、任务或代码里；能用环境变量或 OpenBao 时优先用 `key_env`。
- 在 Magpie 面板里改的、文件里也写了的字段，下次启动会被文件覆盖；只想在面板里改的设置，就不要写进文件。
- 在 Magpie 面板停用某个供应商之前，先把 `claude.model` / `codex.model` / 分组里引用它的模型改掉，否则每次启动都会打 warning。

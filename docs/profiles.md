# 运行配置与端点预设

> 用 profile 和 preset 管理启动 Agent 的命令、模型、端点与凭据变量。

**运行配置**（profile）把启动一个 Agent 要用的命令、模型、模型端点和凭据变量记在一个名字下；**端点预设**（preset）描述一个模型服务的地址和鉴权变量，再按 CLI 生成运行配置。两者都保存在 `~/.team-worker/config.json`，字段名与 ClawTeam 相同，原有配置可以直接沿用。

```bash
team-worker preset list                                  # 内置 13 个预设，本地同名预设覆盖内置的
team-worker preset generate-profile deepseek claude     # 生成运行配置 claude-deepseek
team-worker profile set kimi-cn --command "kimi --config-file ~/.kimi/config.toml" --model kimi-k2
team-worker profile test claude-deepseek                 # 用一句提示实际跑一次，报告命令、退出码和输出
team-worker spawn tmux --team my-team -n alice --profile claude-deepseek --task "..."
team-worker launch my-template -g "..." --profile claude-deepseek
team-worker agent resume my-team alice --profile claude-deepseek   # 续接时重新带上运行配置的变量
```

`spawn` 选用运行配置的顺序：`--profile`；命令写在 `--` 后面时不用任何运行配置；`default_profile`（环境变量 `TEAM_WORKER_DEFAULT_PROFILE` 优先）；只配置了一个时用它；配置了多个却没指定时报错。

运行配置的环境变量位于适配器之后、模板和 `--env` 之前，所以模板和 `--env` 可以覆盖它；`--dry-run` 把这一层显示为 `profile <名字>`。其中 `env` 和 `env_map` 直接设置变量；`base_url` 和 `api_key_env` 只在变量还没设置时，按 CLI 填入对应的变量（claude 为 `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN`，codex 为 `OPENAI_BASE_URL` / `OPENAI_API_KEY`，gemini、kimi 类似），也可以用 `base_url_env`、`api_key_target_env` 指定。命令里没有 `--model` 或 `-m` 时，claude、codex、gemini、kimi、pi 会加上运行配置的模型。

凭据只记环境变量名：`api_key_env` 和 `env_map` 的来源变量在启动时从调用 `spawn` 的环境读取，配置文件里不保存密钥本身。来源变量没有设置或为空时，对应的变量不会注入。

命令一览：`preset list|show|set|set-client|copy|remove|remove-client|generate-profile|bootstrap`，`profile list|show|set|remove|test`。`set` 只修改给出的参数，`--env`、`--env-map`、`--arg` 会整体替换原有值。ClawTeam 的交互式 `profile wizard` 和 `profile doctor` 没有实现。

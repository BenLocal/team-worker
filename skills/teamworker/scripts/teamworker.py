#!/usr/bin/env python3
"""远程操作已部署的 team-worker：包装 team-worker CLI。

- 从 ~/.config/teamworker/remote.env 或环境变量注入服务地址和 Key，只作用于本次调用；
  地址只需写到端口（http://主机:端口），/team-worker-api 自动补上；
- 发件身份固定为操作者（默认 operator），不冒用团队成员；
- 服务器地址加入 NO_PROXY，内网直连；
- 拦截会消费他人收件箱的命令，以及会在本机启动 Agent 的命令；
- 提供只读的 wait：轮询任务状态，不读任何收件箱。

退出码：wait 全部完成 0、超时 4；被拦截或用法错误 2；配置或连接问题 3；
其余命令沿用 team-worker 的退出码。只依赖 Python 3.9+ 标准库。
"""

import argparse
import json
import os
import platform
import shutil
import subprocess
import sys
import time
from urllib.parse import urlparse

CONFIG_FILE = os.path.expanduser("~/.config/teamworker/remote.env")
API_PATH = "/team-worker-api"
DEFAULT_FROM = "operator"
# 本机身份变量：调用者若本身是某个团队的 Agent，不能把它带到远程
LOCAL_IDENTITY = (
    "TEAM_WORKER_AGENT_ID",
    "TEAM_WORKER_AGENT_TYPE",
    "TEAM_WORKER_AGENT_LEADER",
    "TEAM_WORKER_TEAM_NAME",
    "TEAM_WORKER_SOCKET",
    "TEAM_WORKER_DATA_DIR",
)
# 命令前两个词 → 拦截原因
BLOCKED = {
    ("task", "wait"): "会读取并消费 Leader 收件箱里的消息；用本脚本的 wait 等任务完成",
    ("inbox", "receive"): "会消费收件箱里的消息；查看历史用 inbox log",
    ("inbox", "watch"): "会消费收件箱里的消息；查看历史用 inbox log，等任务用 wait",
    (
        "spawn",
    ): "会在本机启动 Agent，不属于远程操作；需要时直接用 team-worker，参考 team-worker skill",
    ("launch",): "会在本机建团队并启动 Agent，不属于远程操作；参考 team-worker skill",
    ("agent", "resume"): "会在本机恢复 Agent 进程，不属于远程操作",
    ("harness", "conduct"): "会在本机启动 Agent，不属于远程操作",
}
DONE = "completed"


class Fail(Exception):
    def __init__(self, message, code):
        super().__init__(message)
        self.code = code


def read_config(path):
    values = {}
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith("#") or "=" not in line:
                    continue
                key, value = line.split("=", 1)
                values[key.strip().removeprefix("export ").strip()] = (
                    value.strip().strip("'\"")
                )
    except FileNotFoundError:
        pass
    return values


def server_url(base):
    """部署的基础地址 → API 地址：补上 /team-worker-api，已带上时不重复。"""
    base = base.strip().rstrip("/")
    if not base:
        raise Fail(
            f"没有设置服务地址：在 {CONFIG_FILE} 写入 TEAM_WORKER_SERVER_URL=http://<主机>:<端口>"
            "（例如 http://172.31.169.115:28088），或设置同名环境变量；"
            f"{API_PATH} 会自动补上。不确定地址时问用户",
            3,
        )
    return base if base.endswith(API_PATH) else base + API_PATH


def build_env():
    env = os.environ.copy()
    for key, value in read_config(env.get("TEAMWORKER_CONFIG", CONFIG_FILE)).items():
        env.setdefault(key, value)
    env["TEAM_WORKER_SERVER_URL"] = server_url(env.get("TEAM_WORKER_SERVER_URL", ""))
    if not env.get("TEAM_WORKER_API_KEY"):
        raise Fail(
            f"没有 API Key：写入 {CONFIG_FILE}（TEAM_WORKER_API_KEY=...，权限 600），"
            "或设置环境变量 TEAM_WORKER_API_KEY",
            3,
        )
    for key in LOCAL_IDENTITY:
        env.pop(key, None)
    env["TEAM_WORKER_AGENT_NAME"] = env.get("TEAMWORKER_FROM") or DEFAULT_FROM
    env["TEAM_WORKER_NO_AUTOSTART"] = "1"
    if not env.get("TEAMWORKER_USE_PROXY"):
        host = urlparse(env["TEAM_WORKER_SERVER_URL"]).hostname or ""
        for key in ("NO_PROXY", "no_proxy"):
            parts = [p for p in env.get(key, "").split(",") if p]
            if host and host not in parts:
                parts.append(host)
            env[key] = ",".join(parts)
    return env


# 本 skill 在 team-worker 仓库的 skills/teamworker/ 下；realpath 穿过 ~/.claude/skills 的软链
REPO_ROOT = os.path.dirname(
    os.path.dirname(os.path.dirname(os.path.dirname(os.path.realpath(__file__))))
)
ARCH = {"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}
OS = {"linux": "linux", "darwin": "darwin", "win32": "windows"}


def dist_name():
    """当前平台在 make dist 产物里的文件名。"""
    os_name = OS.get(sys.platform)
    arch = ARCH.get(platform.machine().lower())
    if not os_name or not arch:
        return None
    return f"team-worker-{os_name}-{arch}" + (".exe" if os_name == "windows" else "")


def find_cli(env):
    """TEAMWORKER_BIN → PATH → 仓库 bin/dist 里当前平台的版本 → 仓库 bin/team-worker。"""
    explicit = env.get("TEAMWORKER_BIN")
    if explicit:
        # 用户指定了就只用它，不悄悄换成别处的 CLI
        if os.path.isfile(explicit) and os.access(explicit, os.X_OK):
            return explicit
        raise Fail(f"TEAMWORKER_BIN 指向的文件不存在或不可执行：{explicit}", 3)
    name = dist_name()
    candidates = [shutil.which("team-worker", path=env.get("PATH"))]
    if name:
        candidates.append(os.path.join(REPO_ROOT, "bin", "dist", name))
    candidates.append(os.path.join(REPO_ROOT, "bin", "team-worker"))
    for path in candidates:
        if path and os.path.isfile(path) and os.access(path, os.X_OK):
            return path
    raise Fail(
        "找不到 team-worker CLI：在 team-worker 仓库根目录执行 make dist（各平台版本输出到 bin/dist/），"
        f"把 {name or '对应平台的文件'} 放进 PATH 并命名为 team-worker，或设置 TEAMWORKER_BIN",
        3,
    )


def command_words(args):
    """跳过全局参数，取命令的前两个词。"""
    words = [a for a in args if not a.startswith("-")]
    return tuple(words[:2])


def blocked_reason(args):
    words = command_words(args)
    for prefix, reason in BLOCKED.items():
        if words[: len(prefix)] == prefix:
            return " ".join(prefix), reason
    return None


def run_json(cli, env, *args):
    p = subprocess.run(
        [cli, "--json", *args], env=env, capture_output=True, text=True, check=False
    )
    if p.returncode != 0:
        msg = (p.stderr or p.stdout).strip()
        code = 3 if "Unauthorized" in msg or "connect" in msg.lower() else 1
        raise Fail(f"team-worker {' '.join(args)} 失败：{msg}", code)
    try:
        return json.loads(p.stdout)
    except ValueError:
        raise Fail(f"team-worker {' '.join(args)} 的输出不是 JSON：{p.stdout[:200]}", 1)


def as_list(value, *keys):
    """--json 输出可能是数组，也可能是包了一层的对象。"""
    if isinstance(value, dict):
        value = next((value[k] for k in keys if isinstance(value.get(k), list)), [])
    return value if isinstance(value, list) else []


def snapshot(cli, env, team, ids):
    if ids:
        return [run_json(cli, env, "task", "get", team, i) for i in ids]
    return as_list(run_json(cli, env, "task", "list", team), "tasks", "items")


def brief(task):
    return {
        k: task.get(k)
        for k in ("id", "subject", "status", "owner", "lockedBy", "blockedBy")
    }


def cmd_wait(cli, env, opts):
    deadline = time.monotonic() + opts.timeout
    seen = {}
    while True:
        tasks = snapshot(cli, env, opts.team, opts.ids)
        for t in tasks:
            b = brief(t)
            if seen.get(b["id"]) != b:
                seen[b["id"]] = b
                print(
                    json.dumps(dict(event="task", **b), ensure_ascii=False), flush=True
                )
        if not tasks:
            raise Fail(f"团队 {opts.team} 没有任务可等", 2)
        if all(t.get("status") == DONE for t in tasks):
            status, code = "completed", 0
        elif time.monotonic() >= deadline:
            status, code = "timeout", 4
        else:
            time.sleep(opts.interval)
            continue
        counts = {}
        for t in tasks:
            counts[t.get("status")] = counts.get(t.get("status"), 0) + 1
        print(
            json.dumps(
                {"event": "result", "status": status, "counts": counts},
                ensure_ascii=False,
            )
        )
        return code


def cmd_doctor(cli, env):
    key = env["TEAM_WORKER_API_KEY"]
    info = {
        "cli": cli,
        "server": env["TEAM_WORKER_SERVER_URL"],
        "apiKey": key[:4] + "…" if len(key) > 8 else "(set)",
        "from": env["TEAM_WORKER_AGENT_NAME"],
        "noProxy": env.get("NO_PROXY", ""),
    }
    teams = as_list(run_json(cli, env, "team", "discover"), "teams", "items")
    info["teams"] = [t.get("name") for t in teams]
    print(json.dumps(info, ensure_ascii=False, indent=2))
    return 0


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    if not argv or argv[0] in ("-h", "--help"):
        print(__doc__.strip())
        print(
            "\n用法：teamworker.py doctor | wait TEAM [TASK_ID...] [--timeout 秒] [--interval 秒]"
            " | <team-worker 命令和参数>"
        )
        return 0 if argv else 2
    try:
        if argv[0] not in ("doctor", "wait"):
            hit = blocked_reason(argv)
            if hit:
                raise Fail(f"远程操作不执行 `{hit[0]}`：{hit[1]}", 2)
        env = build_env()
        cli = find_cli(env)
        if argv[0] == "doctor":
            return cmd_doctor(cli, env)
        if argv[0] == "wait":
            p = argparse.ArgumentParser(prog="teamworker.py wait")
            p.add_argument("team")
            p.add_argument("ids", nargs="*", help="只等这些任务；省略时等团队全部任务")
            p.add_argument(
                "--timeout", type=float, default=600, help="总等待秒数，默认 600"
            )
            p.add_argument(
                "--interval", type=float, default=5, help="轮询间隔秒数，默认 5"
            )
            return cmd_wait(cli, env, p.parse_args(argv[1:]))
        return subprocess.run([cli, *argv], env=env, check=False).returncode
    except Fail as e:
        print(f"错误：{e}", file=sys.stderr)
        return e.code
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    sys.exit(main())

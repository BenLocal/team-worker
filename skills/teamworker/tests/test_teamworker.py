"""teamworker.py 的测试：用假的 team-worker CLI，不连真实部署。

运行：python3 -m unittest discover -s skills/teamworker/tests -v
"""

import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "teamworker.py"

# 假 CLI：记录每次调用的参数和环境；task get/list 按调用次数推进状态。
FAKE_CLI = textwrap.dedent(
    """\
    #!{python}
    import json, os, sys
    state_dir = os.environ["FAKE_STATE"]
    log = os.path.join(state_dir, "calls.jsonl")
    keep = [k for k in os.environ if k.startswith(("TEAM_WORKER_", "NO_PROXY", "no_proxy"))]
    with open(log, "a") as f:
        f.write(json.dumps({{"argv": sys.argv[1:], "env": {{k: os.environ[k] for k in keep}}}}) + "\\n")
    args = [a for a in sys.argv[1:] if a != "--json"]
    plan = json.load(open(os.path.join(state_dir, "plan.json")))
    counter = os.path.join(state_dir, "n")
    n = int(open(counter).read()) if os.path.exists(counter) else 0
    if args[:2] == ["task", "get"]:
        open(counter, "w").write(str(n + 1))
        status = plan["statuses"][min(n, len(plan["statuses"]) - 1)]
        print(json.dumps({{"id": args[3], "subject": "做事", "status": status, "owner": "alice"}}))
    elif args[:2] == ["task", "list"]:
        print(json.dumps(plan.get("list", [])))
    elif args[:2] == ["team", "discover"]:
        print(json.dumps([{{"name": "courselab"}}]))
    elif args[:2] == ["team", "status"]:
        print("ok")
        sys.exit(plan.get("exit", 0))
    else:
        sys.exit(9)
    """
)


class TeamworkerTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.dir = Path(self.tmp.name)
        self.cli = self.dir / "team-worker"
        self.cli.write_text(FAKE_CLI.format(python=sys.executable))
        self.cli.chmod(0o755)
        self.config = self.dir / "remote.env"
        self.config.write_text(
            "# 远程部署\nTEAM_WORKER_SERVER_URL=http://10.0.0.5:28088/\n"
            "TEAM_WORKER_API_KEY='file-key'\n"
        )
        self.plan({"statuses": ["completed"]})
        self.env = {
            "PATH": os.environ["PATH"],
            "HOME": str(self.dir),
            "TEAMWORKER_BIN": str(self.cli),
            "TEAMWORKER_CONFIG": str(self.config),
            "FAKE_STATE": str(self.dir),
            "NO_PROXY": "localhost",
            "TEAM_WORKER_AGENT_ID": "local-agent-id",
        }

    def plan(self, value):
        (self.dir / "plan.json").write_text(json.dumps(value))

    def run_cli(self, *args, **extra_env):
        env = dict(self.env, **extra_env)
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )

    def calls(self):
        log = self.dir / "calls.jsonl"
        if not log.exists():
            return []
        return [json.loads(line) for line in log.read_text().splitlines()]

    def test_passthrough_injects_remote_settings_and_operator_identity(self):
        p = self.run_cli("team", "status", "courselab")
        self.assertEqual(p.returncode, 0, p.stderr)
        call = self.calls()[0]
        self.assertEqual(call["argv"], ["team", "status", "courselab"])
        env = call["env"]
        self.assertEqual(
            env["TEAM_WORKER_SERVER_URL"], "http://10.0.0.5:28088/team-worker-api"
        )
        self.assertEqual(env["TEAM_WORKER_API_KEY"], "file-key")
        self.assertEqual(env["TEAM_WORKER_AGENT_NAME"], "operator")
        self.assertEqual(env["TEAM_WORKER_NO_AUTOSTART"], "1")
        self.assertEqual(env["NO_PROXY"], "localhost,10.0.0.5")
        self.assertNotIn("TEAM_WORKER_AGENT_ID", env)

    def test_environment_overrides_config_file(self):
        self.run_cli(
            "team", "status", "x", TEAM_WORKER_API_KEY="env-key", TEAMWORKER_FROM="ben"
        )
        env = self.calls()[0]["env"]
        self.assertEqual(env["TEAM_WORKER_API_KEY"], "env-key")
        self.assertEqual(env["TEAM_WORKER_AGENT_NAME"], "ben")

    def test_use_proxy_leaves_no_proxy_alone(self):
        self.run_cli("team", "status", "x", TEAMWORKER_USE_PROXY="1")
        self.assertEqual(self.calls()[0]["env"]["NO_PROXY"], "localhost")

    def test_exit_code_is_passed_through(self):
        self.plan({"statuses": ["completed"], "exit": 5})
        self.assertEqual(self.run_cli("team", "status", "x").returncode, 5)

    def test_consuming_and_local_spawn_commands_are_refused_before_running(self):
        for args in (
            ["task", "wait", "t"],
            ["--json", "inbox", "receive", "t"],
            ["inbox", "watch", "t"],
            ["spawn", "tmux", "--team", "t"],
            ["launch", "tpl.toml"],
            ["agent", "resume", "t", "a"],
        ):
            with self.subTest(args=args):
                p = self.run_cli(*args)
                self.assertEqual(p.returncode, 2)
                self.assertIn("远程操作不执行", p.stderr)
        self.assertEqual(self.calls(), [])

    def test_wait_keeps_going_through_blocked_until_completed(self):
        self.plan({"statuses": ["blocked", "pending", "in_progress", "completed"]})
        p = self.run_cli("wait", "courselab", "T1", "--interval", "0.01")
        self.assertEqual(p.returncode, 0, p.stderr)
        events = [json.loads(line) for line in p.stdout.splitlines()]
        self.assertEqual(
            [e["status"] for e in events if e["event"] == "task"],
            ["blocked", "pending", "in_progress", "completed"],
        )
        self.assertEqual(
            events[-1],
            {"event": "result", "status": "completed", "counts": {"completed": 1}},
        )
        # 只读任务，不碰收件箱
        self.assertTrue(all(c["argv"][1:3] == ["task", "get"] for c in self.calls()))

    def test_wait_times_out_with_exit_4(self):
        self.plan({"statuses": ["in_progress"]})
        p = self.run_cli(
            "wait", "courselab", "T1", "--interval", "0.01", "--timeout", "0.05"
        )
        self.assertEqual(p.returncode, 4)
        self.assertEqual(json.loads(p.stdout.splitlines()[-1])["status"], "timeout")

    def test_wait_without_ids_waits_for_all_listed_tasks(self):
        self.plan(
            {
                "statuses": ["completed"],
                "list": [
                    {"id": "a", "status": "completed"},
                    {"id": "b", "status": "completed"},
                ],
            }
        )
        p = self.run_cli("wait", "courselab")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('"counts": {"completed": 2}', p.stdout)

    def test_full_api_url_is_not_suffixed_twice(self):
        self.run_cli(
            "team",
            "status",
            "x",
            TEAM_WORKER_SERVER_URL="http://10.0.0.5:28088/team-worker-api/",
        )
        self.assertEqual(
            self.calls()[0]["env"]["TEAM_WORKER_SERVER_URL"],
            "http://10.0.0.5:28088/team-worker-api",
        )

    def test_missing_server_url_asks_for_it_without_running(self):
        self.config.write_text("TEAM_WORKER_API_KEY=k\n")
        p = self.run_cli("team", "status", "x")
        self.assertEqual(p.returncode, 3)
        self.assertIn("没有设置服务地址", p.stderr)
        self.assertEqual(self.calls(), [])

    def test_missing_key_or_cli_is_reported_as_setup_error(self):
        self.config.write_text("TEAM_WORKER_SERVER_URL=http://10.0.0.5/x\n")
        p = self.run_cli("team", "status", "x")
        self.assertEqual(p.returncode, 3)
        self.assertIn("没有 API Key", p.stderr)
        p = self.run_cli(
            "team",
            "status",
            "x",
            TEAM_WORKER_API_KEY="k",
            TEAMWORKER_BIN=str(self.dir / "missing"),
        )
        self.assertEqual(p.returncode, 3)
        self.assertIn("TEAMWORKER_BIN 指向的文件不存在", p.stderr)

    def test_doctor_lists_teams_and_masks_key(self):
        p = self.run_cli("doctor", TEAM_WORKER_API_KEY="abcdefghijkl")
        self.assertEqual(p.returncode, 0, p.stderr)
        info = json.loads(p.stdout)
        self.assertEqual(info["teams"], ["courselab"])
        self.assertEqual(info["apiKey"], "abcd…")
        self.assertNotIn("abcdefghijkl", p.stdout)


class DistNameTest(unittest.TestCase):
    def load(self):
        spec = importlib.util.spec_from_file_location("teamworker", SCRIPT)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def test_platforms_map_to_make_dist_names(self):
        m = self.load()
        cases = {
            ("linux", "x86_64"): "team-worker-linux-amd64",
            ("linux", "aarch64"): "team-worker-linux-arm64",
            ("darwin", "arm64"): "team-worker-darwin-arm64",
            ("win32", "AMD64"): "team-worker-windows-amd64.exe",
            ("win32", "ARM64"): "team-worker-windows-arm64.exe",
            ("freebsd", "x86_64"): None,
        }
        for (plat, machine), want in cases.items():
            with (
                self.subTest(plat=plat, machine=machine),
                mock.patch.object(m.sys, "platform", plat),
                mock.patch.object(m.platform, "machine", return_value=machine),
            ):
                self.assertEqual(m.dist_name(), want)

    def test_repo_root_resolves_through_symlinked_skill_dir(self):
        with tempfile.TemporaryDirectory() as tmp:
            link = Path(tmp) / "skills-link"
            link.symlink_to(SCRIPT.parents[1])
            p = subprocess.run(
                [
                    sys.executable,
                    "-c",
                    "import runpy,sys; m=runpy.run_path(sys.argv[1]); print(m['REPO_ROOT'])",
                    str(link / "scripts" / "teamworker.py"),
                ],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(p.stdout.strip(), str(SCRIPT.parents[3]))


if __name__ == "__main__":
    unittest.main()

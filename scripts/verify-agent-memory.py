#!/usr/bin/env python3
"""Verify personal rules with a real CLI in isolated data and project directories."""

import argparse
import json
import os
import shutil
import subprocess
import tempfile
import time
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--engine", required=True, choices=["claude", "codex", "pi"])
    parser.add_argument("--bin-dir", default="bin")
    parser.add_argument("--timeout", type=int, default=180)
    parser.add_argument(
        "--with-reflection",
        action="store_true",
        help="Also run and apply one real model reflection",
    )
    parser.add_argument(
        "--reflection-only",
        action="store_true",
        help="Only verify real model reflection",
    )
    args = parser.parse_args()
    binaries = Path(args.bin_dir).resolve()
    worker = str(binaries / "team-worker")
    daemon = str(binaries / "team-workerd")
    engine = shutil.which(args.engine)
    if not engine:
        raise SystemExit(f"Missing CLI: {args.engine}")
    root = Path(tempfile.mkdtemp(prefix="tw-memory-verify-"))
    root.chmod(0o700)
    home = root / "home"
    home.mkdir()
    original_home = Path.home()
    env = {k: v for k, v in os.environ.items() if not k.startswith("TEAM_WORKER_")}
    env.update(
        HOME=str(home),
        TEAM_WORKER_DATA_DIR=str(root / "data"),
        TEAM_WORKER_SOCKET=str(root / "api.sock"),
        TEAM_WORKER_WORKSPACE="never",
    )

    # Only copy the files needed to authenticate and select the existing model.
    # No real credentials are written to the repository or printed in the report.
    sources = {
        "codex": ("CODEX_HOME", original_home / ".codex", ["config.toml", "auth.json"]),
        "claude": (
            "CLAUDE_CONFIG_DIR",
            original_home / ".claude",
            ["settings.json", ".credentials.json"],
        ),
        "pi": (
            "PI_CODING_AGENT_DIR",
            original_home / ".pi/agent",
            ["models.json", "auth.json", "settings.json"],
        ),
    }
    key, fallback, names = sources[args.engine]
    source = Path(os.environ.get(key, str(fallback)))
    isolated = home / args.engine
    isolated.mkdir(mode=0o700)
    for name in names:
        src = source / name
        if src.is_file():
            target = isolated / name
            if name == "settings.json":
                config = json.loads(src.read_text())
                allowed = (
                    {"env", "model"}
                    if args.engine == "claude"
                    else {"defaultProvider", "defaultModel", "defaultThinkingLevel"}
                )
                target.write_text(
                    json.dumps({k: v for k, v in config.items() if k in allowed})
                )
            else:
                shutil.copyfile(src, target)
            target.chmod(0o600)
    env[key] = str(isolated)
    log = (root / "daemon.log").open("w")
    proc = subprocess.Popen(
        [daemon, "--data-dir", str(root / "data"), "--socket", str(root / "api.sock")],
        env=env,
        stdout=log,
        stderr=log,
    )
    report = {"engine": args.engine, "checks": {}, "artifacts": str(root)}

    def cli(*command):
        result = subprocess.run(
            [worker, "--json", *command],
            env=env,
            check=False,
            capture_output=True,
            text=True,
            timeout=args.timeout + 30 if command[:2] == ("memory", "reflect") else 30,
        )
        if result.returncode:
            (root / "last-cli-error.log").write_text(result.stdout + result.stderr)
            raise RuntimeError(
                f"CLI command failed: {command[0:2]} (see private artifacts)"
            )
        try:
            return json.loads(result.stdout)
        except json.JSONDecodeError as exc:
            raise RuntimeError(f"CLI returned non-JSON output: {command[0:2]}") from exc

    def write_rule(marker):
        current = cli("memory", "read", "verification", "worker", "AGENTS.md")[
            "documents"
        ][0]
        return cli(
            "memory",
            "write",
            "verification",
            "worker",
            "AGENTS.md",
            f"# Personal rules\nWhen creating result.txt, its complete contents must be {marker} followed by a newline.\n",
            "--base-hash",
            current["hash"],
        )

    def wait_result(project, marker):
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            target = project / "result.txt"
            record = next(
                (
                    item
                    for item in cli("agent", "list", "verification")
                    if item["name"] == "worker"
                ),
                {},
            )
            if record.get("status") == "exited" and (
                not target.exists() or target.read_text() != marker + "\n"
            ):
                raise RuntimeError(
                    "Native agent exited without the expected artifact; inspect its private log"
                )
            if target.exists() and target.read_text() == marker + "\n":
                # A batch agent may keep monitoring its team after producing
                # the artifact. Capture its exact session before stopping it.
                session = cli("session", "show", "verification", "--agent", "worker")
                if session.get("sessionId"):
                    cli("agent", "stop", "verification", "worker")
                    return
            time.sleep(1)
        raise RuntimeError(
            "Native agent did not produce the expected artifact and exit"
        )

    task = "Create result.txt in the current working directory using the marker specified by your personal rules. Do not contact anyone or create tasks. Then stop."
    try:
        for _ in range(100):
            if (root / "api.sock").exists():
                break
            if proc.poll() is not None:
                raise RuntimeError("Test daemon exited")
            time.sleep(0.05)
        cli("memory", "init", "verification", "worker")
        command = [engine]
        if args.engine == "codex":
            command += ["--no-daemon"]
        if not args.reflection_only:
            write_rule("MEMORY_RULE_A")
            projects = [root / "project-a", root / "project-b"]
            for project in projects:
                project.mkdir()
                cli(
                    "spawn",
                    "subprocess",
                    "--team",
                    "verification",
                    "--agent-name",
                    "worker",
                    "--cwd",
                    str(project),
                    "--task",
                    task,
                    "--",
                    *command,
                )
                wait_result(project, "MEMORY_RULE_A")
            report["checks"]["switch_project"] = "passed"
            write_rule("MEMORY_RULE_B")
            (projects[1] / "result.txt").unlink()
            cli("agent", "resume", "verification", "worker", "--task", task)
            wait_result(projects[1], "MEMORY_RULE_B")
            report["checks"]["resume_updated_rules"] = "passed"
            cli("team", "cleanup", "verification")
            cli("team", "spawn-team", "verification", "--agent-name", "worker")
            current = cli("memory", "read", "verification", "worker", "AGENTS.md")[
                "documents"
            ][0]
            if "MEMORY_RULE_B" not in current["content"]:
                raise RuntimeError("Recreated member lost its rules")
            report["checks"]["recreate_member"] = "passed"
        if args.with_reflection or args.reflection_only:
            cli(
                "memory",
                "write",
                "verification",
                "worker",
                "daily/validation.md",
                "Confirmed enduring user preference for this member: all future reports must use ISO 8601 timestamps in UTC. Preserve this long-term preference.",
                "--base-hash",
                "",
            )
            config = {
                "command": command,
                "targets": ["MEMORY.md"],
                "timeoutSeconds": min(args.timeout, 600),
            }
            cli(
                "memory", "reflect-config", "verification", "worker", json.dumps(config)
            )
            candidate = cli("memory", "reflect", "verification", "worker")["reflection"]
            if candidate["status"] != "pending":
                raise RuntimeError("Real reflection did not propose a candidate")
            cli("memory", "reflect-apply", "verification", "worker", candidate["id"])
            memory = cli("memory", "read", "verification", "worker", "MEMORY.md")[
                "documents"
            ][0]
            if "UTC" not in memory["content"]:
                raise RuntimeError("Applied memory did not preserve the supplied fact")
            report["checks"]["reflection_applied"] = "passed"
    except (RuntimeError, subprocess.TimeoutExpired) as exc:
        report["error"] = str(exc)
    finally:
        try:
            cli("agent", "stop", "verification", "worker")
        except (RuntimeError, subprocess.TimeoutExpired):
            pass
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
        log.close()
        # Remove copied authentication material while retaining test artifacts.
        shutil.rmtree(home)
        (root / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report, indent=2))
    return 1 if "error" in report else 0


if __name__ == "__main__":
    raise SystemExit(main())

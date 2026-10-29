#!/usr/bin/env python3
"""Verify native conversation recall through team-worker's subprocess backend.

Uses the CLI's existing provider configuration and makes two model calls.
Example: python3 scripts/verify-native-sessions.py --client pi -- pi --no-tools
Logs remain in an isolated temporary directory for inspection.
"""

import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time


def native_result(client, text):
    session_id, answer, tools = "", "", 0
    for line in text.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if not isinstance(event, dict):
            continue
        kind = event.get("type")
        if client == "codex":
            if kind == "thread.started":
                session_id = event["thread_id"]
            if kind == "item.completed":
                item = event.get("item", {})
                if item.get("type") == "agent_message":
                    answer = item.get("text", "")
                if item.get("type") in ("command_execution", "mcp_tool_call"):
                    tools += 1
        elif client == "pi":
            if kind == "session":
                session_id = event["id"]
            if kind == "message_end":
                message = event.get("message", {})
                if message.get("role") == "assistant":
                    content = message.get("content", [])
                    answer = "".join(c.get("text", "") for c in content if c.get("type") == "text")
                    tools += sum(c.get("type") == "toolCall" for c in content)
        else:
            if kind == "session":
                session_id = event["sessionId"]
            if kind == "final":
                answer = event.get("text", "")
            if kind == "tool_call":
                tools += 1
    return session_id, answer.strip(), tools


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--bin-dir", default="bin")
    parser.add_argument("--client", required=True, choices=("codex", "dsh", "pi"))
    parser.add_argument("--timeout", type=float, default=120)
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    command = args.command
    if command[:1] == ["--"]:
        command = command[1:]
    if not command:
        parser.error("provide the native CLI command after --")
    binaries = Path(args.bin_dir).resolve()
    root = Path(tempfile.mkdtemp(prefix="tw-native-", dir="/tmp"))
    workspace = root / "workspace"
    workspace.mkdir()
    env = dict(os.environ, TEAM_WORKER_DATA_DIR=str(root / "data"),
               TEAM_WORKER_SOCKET=str(root / "daemon.sock"), TEAM_WORKER_SERVER_URL="",
               TEAM_WORKER_HTTP_ADDR="", TEAM_WORKER_NO_AUTOSTART="1",
               TEAM_WORKER_AGENT_NAME="operator", TEAM_WORKER_USER="")
    for key in ("TEAM_WORKER_RUN_ID", "TEAM_WORKER_AGENT_LEADER", "TEAM_WORKER_START_BLOCKED_FILE"):
        env.pop(key, None)
    team = "native-proof"
    daemon = None

    def cli(*arguments, check=True):
        result = subprocess.run([str(binaries / "team-worker"), "--json", *arguments],
                                env=env, capture_output=True, text=True, timeout=20)
        if check and result.returncode:
            raise AssertionError(f"CLI failed: {result.stdout} {result.stderr}")
        return json.loads(result.stdout) if result.stdout.strip() else None

    def wait_agent():
        deadline = time.monotonic() + args.timeout
        while time.monotonic() < deadline:
            records = cli("agent", "list", team)
            if records and records[0]["status"] == "exited":
                record = records[0]
                assert record.get("exitCode", 0) == 0, f"native process failed: {record.get('exitCode')}; inspect {record['target']}"
                return record
            time.sleep(0.1)
        raise AssertionError(f"native process timed out; inspect {root}")

    try:
        with (root / "daemon.log").open("w") as log:
            daemon = subprocess.Popen([str(binaries / "team-workerd"), "--data-dir", env["TEAM_WORKER_DATA_DIR"]],
                                      env=env, stdout=log, stderr=log)
        deadline = time.monotonic() + 10
        while not (root / "daemon.sock").exists():
            assert daemon.poll() is None and time.monotonic() < deadline, "daemon failed to start"
            time.sleep(0.05)
        codeword = "RECALL-" + secrets.token_hex(8).upper()
        task = f"Remember this codeword for our conversation: {codeword}. Reply with only that codeword. Do not use any tools or send messages."
        cli("spawn", "subprocess", "--team", team, "-n", "leader", "--cwd", str(workspace),
            "--no-skip-permissions", "--task", task, "--", *command)
        first = wait_agent()
        text = Path(first["target"]).read_text()
        native_id, answer, tools = native_result(args.client, text)
        assert answer == codeword and tools == 0, f"initial reply mismatch or tool use; inspect {first['target']}"
        saved = cli("session", "show", team, "--agent", "leader")
        if args.client == "dsh" and not saved["sessionId"]:
            assert native_id, "dsh did not emit a session id"
            cli("session", "save", team, "--agent", "leader", "--session-id", native_id)
            saved = cli("session", "show", team, "--agent", "leader")
        assert saved["sessionId"], "native session id was not captured"
        if native_id:
            assert saved["sessionId"] == native_id, "saved id differs from native id"
        print(f"PASS ({args.client}): initial reply and exact session id; logs: {root}", flush=True)
        offset = len(text)
        cli("agent", "resume", team, "leader", "--task",
            "What was the codeword from our previous conversation? Reply with only that codeword. Do not use any tools or send messages.")
        second = wait_agent()
        after = cli("session", "show", team, "--agent", "leader")
        assert saved["sessionId"] == after["sessionId"], "resume changed the session id"
        assert first["runId"] != second["runId"], "resume reused the run id"
        resumed_text = Path(second["target"]).read_text()
        if second["target"] == first["target"]:
            resumed_text = resumed_text[offset:]
        resumed_id, answer, tools = native_result(args.client, resumed_text)
        if resumed_id:
            assert resumed_id == saved["sessionId"], "native CLI resumed a different session"
        assert answer == codeword and tools == 0, f"recall mismatch or tool use; inspect {second['target']}"
        print(f"PASS ({args.client}): team-worker resume, same session, new run id, exact recall without tools", flush=True)
    finally:
        if daemon is not None and daemon.poll() is None:
            try:
                cli("team", "cleanup", team, "--force", check=False)
            finally:
                daemon.terminate()
                try:
                    daemon.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    daemon.kill()
                    daemon.wait()
        print(f"Evidence: {root}", flush=True)


if __name__ == "__main__":
    main()

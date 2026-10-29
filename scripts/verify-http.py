#!/usr/bin/env python3
"""Verify HTTP authentication and Unix socket compatibility with built binaries."""

import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--bin-dir", default="bin")
    binaries = Path(parser.parse_args().bin_dir).resolve()
    with tempfile.TemporaryDirectory(prefix="tw-http-", dir="/tmp") as temp:
        root = Path(temp)
        key = secrets.token_urlsafe(32)
        env = dict(os.environ, HOME=temp, TEAM_WORKER_DATA_DIR=str(root / "data"),
                   TEAM_WORKER_SOCKET=str(root / "api.sock"), TEAM_WORKER_SERVER_URL="",
                   TEAM_WORKER_API_KEY=key, TEAM_WORKER_HTTP_ADDR="",
                   TEAM_WORKER_NO_AUTOSTART="1", TEAM_WORKER_AGENT_NAME="leader", TEAM_WORKER_USER="")
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            address = f"127.0.0.1:{reservation.getsockname()[1]}"

        def cli(*arguments, remote=True, api_key=key, success=True):
            caller = dict(env, TEAM_WORKER_SERVER_URL="http://" + address if remote else "",
                          TEAM_WORKER_API_KEY=api_key)
            result = subprocess.run([str(binaries / "team-worker"), "--json", *arguments],
                                    env=caller, capture_output=True, text=True, timeout=10)
            assert (result.returncode == 0) == success, (result.stdout, result.stderr)
            return json.loads(result.stdout)

        def start(log, http):
            arguments = [str(binaries / "team-workerd"), "--data-dir", env["TEAM_WORKER_DATA_DIR"]]
            if http:
                arguments += ["--listen", address]
            daemon_env = dict(env, TEAM_WORKER_API_KEY=key if http else "")
            process = subprocess.Popen(arguments, env=daemon_env, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 10
                while time.monotonic() < deadline:
                    assert process.poll() is None, "daemon exited during startup"
                    if (root / "api.sock").exists():
                        probe = subprocess.run([str(binaries / "team-worker"), "--json", "team", "discover"],
                                               env=dict(daemon_env, TEAM_WORKER_API_KEY=""), capture_output=True, timeout=5)
                        if probe.returncode == 0:
                            return process
                    time.sleep(0.05)
                raise AssertionError("daemon did not become ready")
            except BaseException:
                stop(process)
                raise

        def stop(process):
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
                raise AssertionError("daemon did not shut down cleanly")

        with (root / "daemon.log").open("w") as log:
            process = start(log, True)
            try:
                cli("team", "spawn-team", "remote-proof")
                task = cli("task", "create", "remote-proof", "verify remote HTTP")
                local = cli("task", "get", "remote-proof", task["id"], remote=False, api_key="")
                assert local["subject"] == "verify remote HTTP"
                for invalid in ("", "wrong-key"):
                    rejected = cli("team", "discover", api_key=invalid, success=False)
                    assert rejected["error"] == "Unauthorized", rejected
                cli("inbox", "send", "remote-proof", "leader", "remote message")
                messages = cli("inbox", "receive", "remote-proof")
                assert "remote message" in json.dumps(messages)
                print("PASS: HTTP CLI team/task/inbox flow, valid key, missing key, wrong key, socket without key", flush=True)
            finally:
                stop(process)
            process = start(log, False)
            try:
                tasks = cli("task", "list", "remote-proof", remote=False, api_key="")
                assert any(t["id"] == task["id"] for t in tasks)
                print("PASS: socket-only daemon with no API key, existing data and clean shutdown", flush=True)
            finally:
                stop(process)


if __name__ == "__main__":
    main()

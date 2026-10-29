#!/usr/bin/env python3
"""Exercise built CLIs, a real daemon and child processes in isolated state.

Build with make build, then run python3 scripts/verify-sessions.py --bin-dir bin.
Optional --native-claude /path/to/claude also verifies real conversation recall;
it uses that CLI's existing provider configuration and makes two model calls.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--bin-dir", default="bin")
    parser.add_argument("--native-claude")
    parser.add_argument("--backend", choices=("subprocess", "tmux"), default="subprocess")
    args = parser.parse_args()
    binaries = Path(args.bin_dir).resolve()
    with tempfile.TemporaryDirectory(prefix="tw-session-", dir="/tmp") as temp:
        root = Path(temp)
        env = dict(os.environ, TEAM_WORKER_DATA_DIR=str(root / "data"),
                   TEAM_WORKER_SOCKET=str(root / "daemon.sock"),
                   TEAM_WORKER_NO_AUTOSTART="1", TEAM_WORKER_AGENT_NAME="operator",
                   TEAM_WORKER_USER="", ATTEMPT_LOG=str(root / "attempts"))
        env.pop("TEAM_WORKER_RUN_ID", None)
        env.pop("TEAM_WORKER_AGENT_LEADER", None)
        log = (root / "daemon.log").open("a")
        daemon = None

        def cli(*arguments, check=True):
            result = subprocess.run([str(binaries / "team-worker"), "--json", *arguments],
                                    env=env, capture_output=True, text=True, timeout=20)
            if check and result.returncode:
                raise AssertionError(f"CLI failed ({result.returncode}): {result.stdout} {result.stderr}")
            return json.loads(result.stdout) if result.stdout.strip() else None

        def until(probe, timeout=10):
            deadline = time.monotonic() + timeout
            while time.monotonic() < deadline:
                if probe():
                    return
                time.sleep(0.05)
            raise AssertionError("timed out waiting for the expected process state")

        def start_daemon():
            process = subprocess.Popen([str(binaries / "team-workerd"), "--data-dir", env["TEAM_WORKER_DATA_DIR"]],
                                       env=env, stdout=log, stderr=log)
            until(lambda: process.poll() is not None or (root / "daemon.sock").exists())
            assert process.poll() is None, "daemon exited during startup"
            until(lambda: isinstance(cli("team", "discover", check=False), list))
            return process

        try:
            daemon = start_daemon()
            fake_dir = root / "fake"
            fake_dir.mkdir()
            fake = fake_dir / "claude"
            fake.write_text('''#!/bin/sh
printf '%s\\n' "$TEAM_WORKER_RUN_ID" >> "$ATTEMPT_LOG"
if [ "${PROOF_HOLD:-0}" = 1 ]; then
  trap 'exit 0' TERM INT
  while :; do sleep 1; done
fi
exit 7
''')
            fake.chmod(0o755)
            marker = root / "provider-pending"
            marker.touch()
            env["TEAM_WORKER_START_BLOCKED_FILE"] = str(marker)
            cli("team", "spawn-team", "proof")
            cli("spawn", args.backend, "--team", "proof", "-n", "alice", "--cwd", str(root),
                "--keepalive", "--max-restarts", "2", "--restart-delay", "20ms", "--", str(fake))
            time.sleep(0.2)
            assert not (root / "attempts").exists(), "child started before provider setup completed"
            marker.unlink()

            def attempts():
                return (root / "attempts").read_text().splitlines() if (root / "attempts").exists() else []

            until(lambda: len(attempts()) >= 3 and cli("agent", "list", "proof")[0]["status"] == "exited")
            run_ids = attempts()
            assert len(run_ids) == len(set(run_ids)) == 3, run_ids
            saved = cli("session", "show", "proof", "--agent", "alice")
            assert saved["sessionId"], "native session id was not persisted"
            cli("agent", "resume", "proof", "alice", "--keepalive", "--env", "PROOF_HOLD=1")
            until(lambda: len(attempts()) == 4)
            stale = cli("lifecycle", "on-exit", "--team", "proof", "--agent", "alice", "--run-id", run_ids[0], "--code", "9")
            assert not stale["changed"], stale
            assert cli("agent", "list", "proof")[0]["status"] == "running"
            cli("agent", "stop", "proof", "alice")
            time.sleep(0.2)
            assert len(attempts()) == 4, "stopped agent restarted"
            current = cli("agent", "list", "proof")[0]
            assert current["stopRequested"] and current["status"] == "exited", current
            daemon.terminate()
            daemon.wait(timeout=10)
            daemon = start_daemon()
            restored = cli("session", "show", "proof", "--agent", "alice")
            assert restored["sessionId"] == saved["sessionId"], restored
            print(f"PASS ({args.backend}): startup gate, real CLI/daemon, bounded retries, unique run ids, stale exit, stop intent, daemon restart", flush=True)

            if args.native_claude:
                workspace = root / "native"
                workspace.mkdir()
                cli("spawn", "subprocess", "--team", "native-proof", "-n", "leader", "--cwd", str(workspace),
                    "--no-skip-permissions", "--task", "Remember the codeword ORBIT-PINE-729. Reply with only that codeword. Do not call tools.",
                    "--", args.native_claude, "--tools", "")
                until(lambda: cli("agent", "list", "native-proof")[0]["status"] == "exited", timeout=120)
                before = cli("session", "show", "native-proof", "--agent", "leader")
                first = cli("agent", "list", "native-proof")[0]
                assert first.get("exitCode", 0) == 0, "native initial session failed"
                cli("agent", "resume", "native-proof", "leader", "--task", "What was the codeword from our previous conversation? Reply with only that codeword. Do not call tools.")
                until(lambda: cli("agent", "list", "native-proof")[0]["status"] == "exited", timeout=120)
                after = cli("session", "show", "native-proof", "--agent", "leader")
                second = cli("agent", "list", "native-proof")[0]
                assert before["sessionId"] == after["sessionId"] and first["runId"] != second["runId"]
                assert second.get("exitCode", 0) == 0, "native resume failed"
                text = Path(second["target"]).read_text()
                assert text.count("ORBIT-PINE-729") >= 2, "native conversation did not recall its previous codeword"
                print("PASS: native Claude initial session, exact-id resume, conversation recall", flush=True)
        finally:
            if daemon is not None and daemon.poll() is None:
                for team in ("proof", "native-proof"):
                    cli("team", "cleanup", team, "--force", check=False)
                daemon.terminate()
                try:
                    daemon.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    daemon.kill()
                    daemon.wait()
            log.close()


if __name__ == "__main__":
    main()

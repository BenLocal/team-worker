"""Run after `make build` with Playwright installed; uses only an isolated daemon.

Use --serve for a browser driven separately; interrupt to remove the fixture.
PLAYWRIGHT_CHROMIUM_EXECUTABLE can select an existing Chromium installation.
"""

import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import time
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path


@contextmanager
def fixture():
    root = Path(__file__).resolve().parents[4]
    with tempfile.TemporaryDirectory(prefix="tw-progress-") as data:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        url = f"http://127.0.0.1:{port}"
        env = dict(
            os.environ,
            TEAM_WORKER_DATA_DIR=data,
            TEAM_WORKER_SOCKET=f"{data}/daemon.sock",
            TEAM_WORKER_API_KEY="progress-test-key",
            TEAM_WORKER_AGENT_NAME="leader",
        )

        def cli(*args):
            return json.loads(
                subprocess.check_output(
                    [root / "bin/team-worker", "--json", *args], env=env
                )
            )

        daemon = subprocess.Popen(
            [
                root / "bin/team-workerd",
                "--data-dir",
                data,
                "--socket",
                env["TEAM_WORKER_SOCKET"],
                "--listen",
                f"127.0.0.1:{port}",
                "--board",
            ],
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        try:
            for _ in range(100):
                try:
                    with socket.create_connection(("127.0.0.1", port), timeout=1):
                        break
                except OSError:
                    if daemon.poll() is not None:
                        raise RuntimeError("Fixture daemon exited")
                    time.sleep(0.1)
            else:
                raise RuntimeError("Fixture daemon did not start")
            cli("team", "spawn-team", "--no-start", "demo")
            cli("team", "add-member", "demo", "alice")
            goal = cli(
                "task",
                "create",
                "demo",
                "Continuous coverage",
                "--kind",
                "goal",
                "--owner",
                "leader",
            )
            cli(
                "task",
                "create",
                "demo",
                "Long-term subgoal",
                "--kind",
                "goal",
                "--parent",
                goal["id"],
                "--owner",
                "leader",
            )
            old = cli(
                "task",
                "create",
                "demo",
                "Old observation round",
                "--parent",
                goal["id"],
                "--owner",
                "alice",
            )
            cli(
                "task",
                "progress",
                "demo",
                old["id"],
                "--key",
                "old-evidence",
                "--summary",
                "Six hours observed",
            )
            current = cli(
                "task",
                "create",
                "demo",
                "Current observation round",
                "--parent",
                goal["id"],
                "--owner",
                "alice",
            )
            dep = cli(
                "task", "create", "demo", "Final acceptance", "--blocked-by", old["id"]
            )
            cli(
                "task",
                "update",
                "demo",
                old["id"],
                "--superseded-by",
                current["id"],
                "--result",
                "Old round ended early; remaining duration transferred",
                "--artifact",
                "reports/old.md",
            )
            cli("task", "update", "demo", current["id"], "--status", "in_progress")
            cli(
                "task",
                "progress",
                "demo",
                current["id"],
                "--key",
                "prepared",
                "--summary",
                "Checked <script>literal stage</script>",
                "--artifact",
                "reports/prepared.md",
                "--verification",
                "Three input files checked",
            )
            at = datetime.now(UTC) + timedelta(hours=1)
            checkpoint = cli(
                "checkpoint",
                "create",
                "demo",
                current["id"],
                "--key",
                "next-sample",
                "--at",
                at.isoformat(),
                "--result-by",
                (at + timedelta(minutes=30)).isoformat(),
                "--action",
                "Read sample",
                "--expect",
                "Sample report",
            )
            cli(
                "task",
                "progress",
                "demo",
                current["id"],
                "--key",
                "wait-sample",
                "--summary",
                "Prepared the observation",
                "--state",
                "waiting",
                "--reason",
                "Observation window",
                "--next-action",
                "Read the next sample",
                "--checkpoint",
                checkpoint["id"],
            )
            approval = cli(
                "task", "create", "demo", "Approval-bound test", "--owner", "alice"
            )
            cli("task", "update", "demo", approval["id"], "--status", "in_progress")
            request = cli(
                "request",
                "create",
                "demo",
                "Permit a bounded test",
                "--body",
                "Decide the test scope",
                "--human",
            )
            cli(
                "task",
                "progress",
                "demo",
                approval["id"],
                "--key",
                "wait-approval",
                "--summary",
                "Test prepared",
                "--state",
                "waiting",
                "--reason",
                "Scope decision",
                "--next-action",
                "Read the decision",
                "--request",
                request["requestId"],
            )
            missing = cli("task", "create", "demo", "Missing input", "--owner", "alice")
            cli(
                "task",
                "progress",
                "demo",
                missing["id"],
                "--key",
                "missing-input",
                "--summary",
                "No sample available",
                "--state",
                "waiting",
                "--reason",
                "Needs an input sample",
                "--next-action",
                "Inspect a supplied sample",
            )
            manifest = {
                "url": url,
                "key": env["TEAM_WORKER_API_KEY"],
                "socket": env["TEAM_WORKER_SOCKET"],
                "goal": goal["id"],
                "old": old["id"],
                "current": current["id"],
                "dependent": dep["id"],
                "request": request["requestId"],
                "checkpoint": checkpoint["id"],
                "approval": approval["id"],
            }
            yield cli, manifest
        finally:
            daemon.terminate()
            try:
                daemon.wait(timeout=10)
            except subprocess.TimeoutExpired:
                daemon.kill()
                daemon.wait()


def run_browser(cli, m):
    from playwright.sync_api import expect, sync_playwright

    with sync_playwright() as p:
        browser = p.chromium.launch(
            headless=True,
            executable_path=os.environ.get("PLAYWRIGHT_CHROMIUM_EXECUTABLE"),
        )
        try:
            page = browser.new_page(viewport={"width": 1440, "height": 1000})
            errors = []
            page.on("pageerror", lambda error: errors.append(str(error)))
            page.goto(m["url"])
            page.get_by_label("Access key").fill(m["key"])
            page.get_by_role("button", name="Sign in").click()
            page.wait_for_load_state("networkidle")
            expect(page.locator("#goals")).to_contain_text("Continuous coverage")
            expect(page.locator("#goals")).to_contain_text("Long-term goals (2)")
            expect(page.locator("#goals")).to_contain_text(
                "0/1 batches completed; 1 superseded"
            )
            expect(
                page.locator("#board .subject").filter(has_text="Continuous coverage")
            ).to_have_count(0)
            card = page.locator(".task").filter(
                has=page.get_by_text("Current observation round", exact=True)
            )
            expect(card).to_contain_text("Observation window")
            expect(card).to_contain_text("Next observation:")
            expect(card).to_contain_text("Read the next sample")
            card.locator(":scope > summary").click()
            expect(card).to_contain_text("Stage history (2)")
            expect(card).to_contain_text("Checked <script>literal stage</script>")
            expect(card.locator("script")).to_have_count(0)
            expect(card).to_contain_text("Three input files checked")
            old = page.locator(".task").filter(
                has=page.get_by_text("Old observation round", exact=True)
            )
            expect(old).to_contain_text("Superseded by Current observation round")
            expect(old).to_contain_text("Acceptance was not completed")
            old.locator(":scope > summary").click()
            expect(old).to_contain_text("Six hours observed")
            expect(old).to_contain_text("reports/old.md")
            expect(page.locator("#board")).to_contain_text("No registered trigger")
            approval = page.locator(".task").filter(
                has=page.get_by_text("Approval-bound test", exact=True)
            )
            expect(approval).to_contain_text("pending")
            cli(
                "request",
                "decide",
                "demo",
                m["request"],
                "--reject",
                "-m",
                "Do not execute",
            )
            expect(approval).to_contain_text("rejected", timeout=15000)
            expect(approval).to_contain_text("Ready for follow-up")
            cli(
                "checkpoint",
                "cancel",
                "demo",
                m["checkpoint"],
                "--reason",
                "Window canceled",
            )
            expect(card).to_contain_text("canceled", timeout=15000)
            expect(card).not_to_contain_text("Next observation:")
            expect(card).to_have_attribute("open", "")
            assert cli("task", "get", "demo", m["dependent"])["blockedBy"] == [
                m["current"]
            ]
            page.screenshot(
                path=os.environ.get(
                    "TASK_PROGRESS_SCREENSHOT", "/tmp/tw-progress-board.png"
                ),
                full_page=True,
            )
            page.set_viewport_size({"width": 390, "height": 844})
            expect(card).to_be_visible()
            for selector in ("#goals", "#board"):
                assert page.locator(selector).evaluate(
                    "e => e.scrollWidth <= e.clientWidth"
                )
                assert (
                    page.locator(selector).bounding_box()["x"]
                    + page.locator(selector).bounding_box()["width"]
                    <= 390
                )
            page.screenshot(
                path=os.environ.get(
                    "TASK_PROGRESS_SCREENSHOT", "/tmp/tw-progress-board.png"
                ).replace(".png", "-mobile.png"),
                full_page=True,
            )
            assert not errors, errors
            print(
                "PASS: goals, stage history, safe text, live waits, supersession, preserved dependencies, narrow layout"
            )
        finally:
            browser.close()


def main():
    with fixture() as (cli, manifest):
        if "--serve" in sys.argv:
            print(json.dumps(manifest), flush=True)
            signal.pause()
        else:
            run_browser(cli, manifest)


if __name__ == "__main__":
    main()

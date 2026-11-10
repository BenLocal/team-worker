"""Build with `make build`, then run this script with Playwright installed.
Uses an isolated daemon and database; no existing team data is changed.
"""

import json
import os
import socket
import subprocess
import tempfile
import time
from pathlib import Path

from playwright.sync_api import expect, sync_playwright


def main():
    root = Path(__file__).resolve().parents[4]
    with tempfile.TemporaryDirectory(prefix="tw-results-") as data:
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        url = f"http://127.0.0.1:{port}"
        env = dict(
            os.environ,
            TEAM_WORKER_DATA_DIR=data,
            TEAM_WORKER_SOCKET=f"{data}/daemon.sock",
            TEAM_WORKER_API_KEY="result-test-key",
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
                        raise RuntimeError("Preview daemon exited")
                    time.sleep(0.1)
            else:
                raise RuntimeError("Preview daemon did not start")
            cli("team", "spawn-team", "demo")
            cli("team", "add-member", "demo", "alice")
            cli("team", "add-member", "demo", "bob")
            task = cli("task", "create", "demo", "Export report", "--owner", "alice")
            cli(
                "task",
                "update",
                "demo",
                task["id"],
                "--status",
                "completed",
                "--result",
                "Exported <script>literal text</script>",
                "--artifact",
                "reports/output.csv",
                "--artifact",
                "https://example.com/report",
                "--artifact",
                "javascript:alert(1)",
                "--verification",
                "Passed: 12 rows checked",
            )
            for i in range(20):
                earlier = cli(
                    "task", "create", "demo", f"Earlier task {i}", "--owner", "bob"
                )
                cli("task", "update", "demo", earlier["id"], "--status", "completed")
            draft = cli("task", "create", "demo", "Unfinished work", "--owner", "alice")
            cli("task", "update", "demo", draft["id"], "--result", "Draft result")
            old = cli(
                "task",
                "create",
                "demo",
                "Legacy task",
                "--owner",
                "bob",
                "--description",
                "Recorded outcome from the previous task",
            )
            cli("task", "update", "demo", old["id"], "--status", "completed")
            with sync_playwright() as p:
                browser = p.chromium.launch(
                    headless=True,
                    executable_path=os.environ.get("PLAYWRIGHT_CHROMIUM_EXECUTABLE"),
                )
                try:
                    page = browser.new_page()
                    errors = []
                    page.on("pageerror", lambda error: errors.append(str(error)))
                    page.goto(url)
                    page.get_by_label("Access key").fill(env["TEAM_WORKER_API_KEY"])
                    page.get_by_role("button", name="Sign in").click()
                    page.get_by_role("tab", name="Results", exact=True).click()
                    page.wait_for_load_state("networkidle")
                    alice = page.locator(".agent-results").filter(
                        has=page.get_by_role("heading", name="alice", exact=True)
                    )
                    bob = page.locator(".agent-results").filter(
                        has=page.get_by_role("heading", name="bob", exact=True)
                    )
                    expect(alice).to_contain_text(
                        "Exported <script>literal text</script>"
                    )
                    expect(page.locator(".agent-results")).to_have_count(3)
                    expect(page.locator(".result-report:visible")).to_have_count(0)
                    expect(alice.locator(".result-subject")).to_have_text(
                        "Export report"
                    )
                    expect(bob.locator(".result-subject")).to_have_text("Legacy task")
                    expect(
                        bob.get_by_text("Earlier task 0", exact=True)
                    ).not_to_be_visible()
                    assert page.locator(".result-groups").bounding_box()["height"] < 400
                    alice.locator(":scope > summary").click()
                    expect(
                        alice.get_by_text("reports/output.csv", exact=True)
                    ).to_be_visible()
                    expect(alice).to_contain_text("Passed: 12 rows checked")
                    expect(alice.locator("a")).to_have_attribute(
                        "href", "https://example.com/report"
                    )
                    expect(alice.locator("script")).to_have_count(0)
                    bob.locator(":scope > summary").click()
                    expect(
                        bob.get_by_text(
                            "Recorded outcome from the previous task", exact=True
                        )
                    ).to_be_visible()
                    bob.locator(".result-history > summary").click()
                    expect(
                        bob.get_by_text("Earlier task 0", exact=True)
                    ).to_be_visible()
                    bob.locator(":scope > summary").click()
                    expect(
                        page.locator(".agent-results").filter(
                            has=page.get_by_role("heading", name="leader", exact=True)
                        )
                    ).to_contain_text("No completed tasks yet")
                    cli(
                        "task",
                        "update",
                        "demo",
                        task["id"],
                        "--result",
                        "Updated report",
                    )
                    expect(alice).to_contain_text("Updated report", timeout=15000)
                    expect(alice).to_have_attribute("open", "")
                    expect(
                        alice.get_by_text("Not reported", exact=True)
                    ).to_be_visible()
                    expect(alice.locator("a")).to_have_count(0)
                    page.reload()
                    expect(
                        page.get_by_role("tab", name="Results", exact=True)
                    ).to_have_attribute("aria-selected", "true")
                    expect(alice).to_contain_text("Updated report")
                    page.set_viewport_size({"width": 390, "height": 844})
                    expect(alice).to_be_visible()
                    alice.locator(":scope > summary").click()
                    expect(
                        alice.get_by_text("Not reported", exact=True)
                    ).to_be_visible()
                    assert page.locator("#results").evaluate(
                        "e => e.scrollWidth <= e.clientWidth"
                    )
                    assert not errors, errors
                    print(
                        "PASS: latest-only rows, collapsed history, task notes, live refresh, safe links, narrow layout"
                    )
                finally:
                    browser.close()
        finally:
            daemon.terminate()
            daemon.wait(timeout=10)


if __name__ == "__main__":
    main()

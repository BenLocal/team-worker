"""Verify online processes, assignments and protocol receipts in an isolated daemon."""

import json
import os
from datetime import UTC, datetime, timedelta
from pathlib import Path
from urllib.request import ProxyHandler, Request, build_opener

from playwright.sync_api import expect, sync_playwright
from task_progress_test import fixture


def main():
    with fixture() as (cli, m):

        def api(path, body):
            req = Request(
                m["url"] + "/v1/teams/demo/" + path,
                data=json.dumps(body).encode(),
                headers={
                    "Authorization": "Bearer " + m["key"],
                    "Content-Type": "application/json",
                },
            )
            with build_opener(ProxyHandler({})).open(req, timeout=10) as response:
                return json.load(response)

        for name in ("bob", "carol", "dave"):
            cli("team", "add-member", "demo", name)
            log = Path(m["socket"]).parent / (name + ".log")
            log.write_text("Isolated test process is online.\n")
            api(
                "agents",
                {
                    "name": name,
                    "runId": name + "-run",
                    "backend": "subprocess",
                    "pid": os.getpid(),
                    "target": str(log),
                },
            )
        at = datetime.now(UTC) + timedelta(hours=2)
        for name in ("carol", "dave"):
            task = cli("task", "create", "demo", "Observe " + name, "--owner", name)
            cli("task", "update", "demo", task["id"], "--status", "in_progress")
            cp = cli(
                "checkpoint",
                "create",
                "demo",
                task["id"],
                "--key",
                "sample-" + name,
                "--at",
                at.isoformat(),
                "--result-by",
                (at + timedelta(hours=1)).isoformat(),
                "--action",
                "Read sample",
                "--expect",
                "Report",
            )
            if name == "carol":
                cli(
                    "task",
                    "progress",
                    "demo",
                    task["id"],
                    "--key",
                    "wait",
                    "--summary",
                    "Prepared",
                    "--state",
                    "waiting",
                    "--reason",
                    "Scheduled observation",
                    "--next-action",
                    "Read sample",
                    "--checkpoint",
                    cp["id"],
                )
        with sync_playwright() as p:
            browser = p.chromium.launch(
                headless=True,
                executable_path=os.environ.get("PLAYWRIGHT_CHROMIUM_EXECUTABLE"),
            )
            try:
                page = browser.new_page(viewport={"width": 1440, "height": 1000})
                errors = []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.goto(m["url"] + "?team=demo&view=terminals")
                page.get_by_label("Access key").fill(m["key"])
                page.get_by_role("button", name="Sign in").click()
                page.get_by_role("tab", name="All", exact=True).click()
                bob = page.locator('.term-card[data-agent="bob"]')
                carol = page.locator('.term-card[data-agent="carol"]')
                dave = page.locator('.term-card[data-agent="dave"]')
                expect(bob.locator(".term-work")).to_have_text("Standby")
                expect(bob.locator(".term-activity")).to_have_text(
                    "Process online · Activity unknown"
                )
                expect(bob.locator(".term-head .dot")).to_have_class("dot alive")
                expect(carol.locator(".term-work")).to_contain_text(
                    "Waiting on registered triggers"
                )
                expect(carol.locator(".term-work")).to_contain_text("Next observation:")
                expect(dave.locator(".term-work")).to_contain_text(
                    "Needs stage report (1)"
                )
                expect(dave.locator(".term-task")).to_have_text(
                    "Assigned tasks: Observe dave"
                )
                expect(bob.locator(".term-protocol")).to_contain_text("update pending")
                ctx = cli(
                    "agent", "protocol", "show", "demo", "bob", "--run-id", "bob-run"
                )
                cli(
                    "agent",
                    "protocol",
                    "ack",
                    "demo",
                    "bob",
                    "--run-id",
                    "bob-run",
                    "--version",
                    ctx["version"],
                )
                expect(bob.locator(".term-protocol")).to_contain_text(
                    "reading acknowledged", timeout=15000
                )
                page.screenshot(
                    path=os.environ.get(
                        "MEMBER_WORK_SCREENSHOT", "/tmp/tw-member-work.png"
                    ),
                    full_page=True,
                )
                page.set_viewport_size({"width": 390, "height": 844})
                expect(bob.locator(".term-activity")).to_be_visible()
                assert bob.locator(".term-work").evaluate(
                    "e => e.scrollWidth <= e.clientWidth"
                )
                assert not errors, errors
                print(
                    "PASS: online/unknown activity, standby, registered wait, missing stage, assigned tasks, live protocol acknowledgment"
                )
            finally:
                browser.close()


if __name__ == "__main__":
    main()

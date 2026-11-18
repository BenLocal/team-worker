"""Run after make build with Playwright and a Chromium executable."""

import json
import os
import socket
import subprocess
import tempfile
import time
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import ProxyHandler, Request, build_opener

from playwright.sync_api import expect, sync_playwright

ROOT = Path(__file__).resolve().parents[4]
KEY = "board-chat-test-key"


def run(data_dir):
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    base = f"http://127.0.0.1:{port}"
    opener = build_opener(ProxyHandler({}))
    daemon = subprocess.Popen(
        [
            ROOT / "bin/team-workerd",
            "--data-dir",
            data_dir,
            "--socket",
            f"{data_dir}/api.sock",
            "--listen",
            f"127.0.0.1:{port}",
            "--board",
        ],
        env=dict(os.environ, TEAM_WORKER_API_KEY=KEY, TEAM_WORKER_WEB_BASE_PATH=""),
        stdout=subprocess.DEVNULL,
    )

    def api(path, data=None):
        request = Request(
            base + path,
            data=json.dumps(data).encode() if data is not None else None,
            headers={
                "Authorization": f"Bearer {KEY}",
                "Content-Type": "application/json",
            },
        )
        with opener.open(request, timeout=2) as response:
            return json.load(response)

    try:
        for _ in range(100):
            if daemon.poll() is not None:
                raise RuntimeError("Fixture daemon exited")
            try:
                api("/v1/health")
                break
            except OSError:
                time.sleep(0.05)
        else:
            raise RuntimeError("Fixture daemon did not start")

        for team in ("alpha", "beta"):
            api("/v1/teams", {"name": team, "leaderName": "leader"})
        account = {
            "provider": "feishu",
            "tenant": "app-scoped",
            "application": "cli_example",
            "appliedRevision": 0,
        }

        try:
            opener.open(base + "/api/chat/settings", timeout=2)
        except HTTPError as error:
            assert error.code == 401
        else:
            raise AssertionError("Chat settings were accessible without authentication")

        with sync_playwright() as playwright:
            browser = playwright.chromium.launch(
                executable_path=os.environ.get("PLAYWRIGHT_CHROMIUM_EXECUTABLE"),
                args=["--no-proxy-server"],
            )
            page = browser.new_page(viewport={"width": 1280, "height": 720})
            page.goto(f"{base}/?k={KEY}", wait_until="domcontentloaded")
            trigger = page.get_by_role("button", name="Settings", exact=True)
            trigger.click()
            expect(
                page.get_by_role("heading", name="Feishu integration")
            ).to_be_visible()
            expect(page.get_by_text("No Feishu application connected")).to_be_visible()
            app_id = page.get_by_label("App ID", exact=True)
            app_secret = page.get_by_label("App Secret", exact=True)
            expect(app_id).to_be_editable()
            expect(app_secret).to_have_attribute("type", "password")

            def save_app(route):
                assert route.request.post_data_json == {
                    "appId": "cli_example",
                    "appSecret": "example-only",
                }
                route.fulfill(
                    json={"appId": "cli_example", "managedByEnvironment": False}
                )

            page.route("**/api/chat/feishu/app", save_app)
            app_id.fill("cli_example")
            app_secret.fill("example-only")
            page.get_by_role("button", name="Save and connect").click()
            expect(
                page.get_by_label("Feishu account allowed to message all teams")
            ).to_be_visible()
            expect(app_secret).to_have_count(0)
            page.unroute("**/api/chat/feishu/app", save_app)
            dialog = page.get_by_role("dialog")
            bounds = dialog.bounding_box()
            assert bounds == {"x": 0, "y": 0, "width": 1280, "height": 720}, bounds
            page.set_viewport_size({"width": 390, "height": 660})
            bounds = dialog.bounding_box()
            assert bounds == {"x": 0, "y": 0, "width": 390, "height": 660}, bounds
            page.set_viewport_size({"width": 1280, "height": 720})
            api("/v1/chat/connections", dict(account, connected=False))
            expect(page.get_by_role("status").first).to_contain_text(
                "Feishu offline", timeout=8000
            )
            field = page.get_by_label("Feishu account allowed to message all teams")
            field.fill("ou_example")
            expect(dialog.locator("input")).to_have_count(1)
            page.get_by_role("button", name="Save settings").click()
            expect(page.get_by_text("Saved version 1")).to_be_visible()
            page.get_by_role("button", name="Close", exact=True).click()
            expect(trigger).to_be_focused()

            page.get_by_role("button", name="Collapse sidebar").click()
            expect(trigger).to_be_visible()
            api(
                "/v1/chat/connections", dict(account, connected=True, appliedRevision=1)
            )
            page.reload()
            trigger.click()
            expect(
                page.get_by_label("Feishu account allowed to message all teams")
            ).to_have_value("ou_example")
            expect(page.get_by_text("Settings applied")).to_be_visible()
            expect(page.get_by_label("App Secret", exact=True)).to_have_count(0)
            page.get_by_role(
                "button", name="Application credentials", exact=True
            ).click()
            expect(page.get_by_label("App ID", exact=True)).to_have_value("cli_example")
            expect(page.get_by_label("App Secret", exact=True)).to_have_attribute(
                "type", "password"
            )
            expect(page.get_by_label("App Secret", exact=True)).to_have_value("")
            page.get_by_role("button", name="Cancel", exact=True).click()
            expect(
                page.get_by_label("Feishu account allowed to message all teams")
            ).to_have_value("ou_example")
            cookies = "; ".join(
                f"{item['name']}={item['value']}" for item in page.context.cookies()
            )
            cross_origin = Request(
                base + "/api/chat/settings",
                data=b"{}",
                method="PUT",
                headers={
                    "Cookie": cookies,
                    "Sec-Fetch-Site": "cross-site",
                    "Content-Type": "application/json",
                },
            )
            try:
                opener.open(cross_origin, timeout=2)
            except HTTPError as error:
                assert error.code == 403
            else:
                raise AssertionError("A cross-site settings update was accepted")
            browser.close()

        saved = api("/v1/chat/settings")
        assert saved["revision"] == 1
        assert saved["feishuSender"]["openId"] == "ou_example"
        assert saved["bindings"] == []
        print(
            "Verified protected settings, sidebar access, persistence, focus and connection status."
        )
    finally:
        daemon.terminate()
        daemon.wait(timeout=10)


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="tw-chat-") as directory:
        run(directory)

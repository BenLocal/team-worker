"""Run after make build with Playwright and a Chromium executable."""

import hashlib
import hmac
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

        for path in ("/api/chat/settings", "/api/settings/vikunja"):
            try:
                opener.open(base + path, timeout=2)
            except HTTPError as error:
                assert error.code == 401
            else:
                raise AssertionError(f"{path} was accessible without authentication")

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
            settings_page = page.get_by_role("main")
            expect(
                settings_page.get_by_role("heading", name="Settings")
            ).to_be_visible()
            expect(page.get_by_role("dialog")).to_have_count(0)
            expect(page.locator(".settings-rail")).to_be_visible()
            expect(
                page.get_by_role("navigation", name="Teams and members")
            ).to_be_visible()
            page.set_viewport_size({"width": 390, "height": 660})
            expect(page.locator(".settings-inline")).to_be_visible()
            expect(page.locator(".settings-rail")).to_be_hidden()
            page.set_viewport_size({"width": 1280, "height": 720})
            api("/v1/chat/connections", dict(account, connected=False))
            expect(page.get_by_role("status").first).to_contain_text(
                "Feishu offline", timeout=8000
            )
            field = page.get_by_label("Feishu account allowed to message all teams")
            field.fill("ou_example")
            expect(
                settings_page.get_by_role("region", name="Feishu integration").locator(
                    "input"
                )
            ).to_have_count(1)
            page.get_by_role("button", name="Save settings").click()
            expect(page.get_by_text("Saved version 1")).to_be_visible()
            page.get_by_role("button", name="Back", exact=True).click()
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

            sections = page.get_by_role("navigation", name="Settings sections")
            sections.get_by_role("button", name="Vikunja", exact=True).click()
            hooks = page.locator(".vikunja-settings")
            expect(
                hooks.get_by_role("combobox", name="Team", exact=True)
            ).to_have_value("alpha")
            expect(hooks.get_by_label("Webhook URL")).to_have_value(
                base + "/api/hooks/vikunja/alpha"
            )
            hooks.get_by_label("Signing secret").fill("example-webhook-secret")
            hooks.get_by_role("combobox", name="Events to deliver").select_option(
                "allowlist"
            )
            hooks.get_by_label("Allowed events").fill("task.created\ntask.comment.*")
            hooks.get_by_role("button", name="Save webhook").click()
            expect(hooks.get_by_text("Webhook saved")).to_be_visible()
            expect(hooks.get_by_label("Signing secret")).to_have_value("")
            saved_hooks = page.request.get(base + "/api/settings/vikunja")
            assert saved_hooks.ok
            assert saved_hooks.json()[0] == {
                "team": "alpha",
                "hasSecret": True,
                "events": ["task.created", "task.comment.*"],
            }

            for name, status in (
                ("task.created", 202),
                ("task.comment.created", 202),
                ("task.updated", 204),
            ):
                body = json.dumps({"event_name": name, "data": {}}).encode()
                signature = hmac.new(
                    b"example-webhook-secret", body, hashlib.sha256
                ).hexdigest()
                request = Request(
                    base + "/api/hooks/vikunja/alpha",
                    data=body,
                    headers={
                        "X-Vikunja-Signature": signature,
                        "Content-Type": "application/json",
                    },
                )
                with opener.open(request, timeout=2) as response:
                    assert response.status == status

            assert "view=settings&tab=vikunja" in page.url
            page.reload()
            expect(
                sections.get_by_role("button", name="Vikunja", exact=True)
            ).to_have_attribute("aria-current", "page")
            expect(hooks.get_by_label("Allowed events")).to_have_value(
                "task.created\ntask.comment.*"
            )
            hooks.get_by_role("combobox", name="Team", exact=True).select_option("beta")
            expect(
                hooks.get_by_role("combobox", name="Events to deliver")
            ).to_have_value("all")
            page.get_by_role("button", name="alpha", exact=True).click()
            expect(
                settings_page.get_by_role("heading", name="alpha", exact=True)
            ).to_be_visible()
            expect(settings_page.get_by_role("heading", name="Settings")).to_have_count(
                0
            )
            browser.close()

        saved = api("/v1/chat/settings")
        assert saved["revision"] == 1
        assert saved["feishuSender"]["openId"] == "ou_example"
        assert saved["bindings"] == []
        print(
            "Verified Feishu and Vikunja settings, event filtering, persistence and focus."
        )
    finally:
        daemon.terminate()
        daemon.wait(timeout=10)


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="tw-chat-") as directory:
        run(directory)

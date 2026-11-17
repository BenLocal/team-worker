"""Verify approval tabs stay clickable when a long list overflows the page."""

import os
import re

from playwright.sync_api import expect, sync_playwright
from task_progress_test import fixture


def assert_tabs_uncovered(page):
    tabs = page.get_by_role("tablist", name="Approvals", exact=True)
    for label in ("Requests", "Plans", "Join requests"):
        button = tabs.get_by_role(
            "tab", name=re.compile("^" + re.escape(label) + r"(?:\s*\d+)?$")
        )
        assert button.evaluate("""e => {
            const r = e.getBoundingClientRect();
            const hit = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2);
            return e === hit || e.contains(hit);
        }"""), f"Approval tab is clipped or covered: {label}"


def main():
    with fixture() as (cli, m):
        for i in range(8):
            cli(
                "request",
                "create",
                "demo",
                f"Review batch {i}",
                "--body",
                "Check the recorded evidence before deciding. " * 12,
                "--human",
            )
        with sync_playwright() as p:
            browser = p.chromium.launch(
                headless=True,
                executable_path=os.environ.get("PLAYWRIGHT_CHROMIUM_EXECUTABLE"),
            )
            try:
                page = browser.new_page(viewport={"width": 1440, "height": 900})
                errors = []
                page.on("pageerror", lambda error: errors.append(str(error)))
                page.goto(m["url"] + "?team=demo&view=approvals")
                page.get_by_label("Access key").fill(m["key"])
                page.get_by_role("button", name="Sign in", exact=True).click()
                tabs = page.get_by_role("tablist", name="Approvals", exact=True)
                expect(tabs).to_be_visible()
                for width in (1440, 1000, 390):
                    page.set_viewport_size({"width": width, "height": 900})
                    tabs.get_by_role(
                        "tab", name=re.compile(r"^Requests(?:\s*\d+)?$")
                    ).click(force=True)
                    page.locator("#approvals").evaluate("e => e.scrollTop = 0")
                    if width >= 1000:
                        assert page.locator("#approvals").evaluate(
                            "e => e.scrollHeight > e.clientHeight"
                        ), "Fixture must overflow"
                    assert_tabs_uncovered(page)
                    for label in ("Plans", "Join requests", "Requests"):
                        tabs.get_by_role(
                            "tab",
                            name=re.compile("^" + re.escape(label) + r"(?:\s*\d+)?$"),
                        ).click(timeout=3000)
                        expect(
                            tabs.get_by_role(
                                "tab",
                                name=re.compile(
                                    "^" + re.escape(label) + r"(?:\s*\d+)?$"
                                ),
                            )
                        ).to_have_attribute("aria-selected", "true")
                    page.screenshot(
                        path=os.environ.get(
                            "APPROVALS_SCREENSHOT_PREFIX", "/tmp/tw-approvals-after"
                        )
                        + f"-{width}.png",
                        full_page=True,
                    )
                assert not errors, errors
                print(
                    "PASS: approval tabs stay uncovered and switch panels with overflowing content at desktop and narrow widths"
                )
            finally:
                browser.close()


if __name__ == "__main__":
    main()

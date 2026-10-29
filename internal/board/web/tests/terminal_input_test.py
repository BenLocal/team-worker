"""Start `npm run dev -- --port 5175` in the board web directory, then run
`python3 tests/terminal_input_test.py` with Playwright and Chromium installed.
"""

import argparse
from playwright.sync_api import sync_playwright


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--url", default="http://127.0.0.1:5175")
    args = parser.parse_args()
    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True, args=["--no-sandbox", "--no-proxy-server"])
        page = browser.new_page(viewport={"width": 1280, "height": 900}, device_scale_factor=2)
        response = page.goto(args.url + "/tests/terminal-input.html")
        assert response.status == 200, response.status
        page.wait_for_function("window.fixture !== undefined", timeout=10000)
        cdp = page.context.new_cdp_session(page)

        for row, col, font in [(4, 12, 13), (4, 25, 13), (9, 7, 17)]:
            page.evaluate("font => fixture.card.terminal.options.fontSize = font", font)
            page.evaluate("""([row, col]) => new Promise(resolve => {
                fixture.card.terminal.write(`\x1b[${row};${col}H`, resolve);
            })""", [row, col])
            page.evaluate("fixture.card.terminal.focus()")
            page.wait_for_function("document.activeElement.tagName === 'TEXTAREA'", timeout=3000)
            geometry = page.evaluate("""() => {
                const canvas = fixture.card.liveHost.querySelector('canvas');
                const input = fixture.card.liveHost.querySelector('textarea');
                const a = input.getBoundingClientRect(), b = canvas.getBoundingClientRect();
                return { x: a.x - b.x, y: a.y - b.y,
                    cellWidth: b.width / fixture.card.terminal.cols,
                    cellHeight: b.height / fixture.card.terminal.rows };
            }""")
            assert abs(geometry["x"] - (col - 1) * geometry["cellWidth"]) < 2, geometry
            assert abs(geometry["y"] - (row - 1) * geometry["cellHeight"]) < 2, geometry

        page.evaluate("fixture.received.length = 0")
        page.keyboard.type("abc")
        assert page.evaluate("fixture.received.join('')") == "abc"
        page.evaluate("fixture.received.length = 0")
        cdp.send("Input.imeSetComposition", {
            "text": "中文", "selectionStart": 2, "selectionEnd": 2,
        })
        assert page.locator("textarea").input_value() == "中文"
        assert page.locator("textarea").evaluate("e => getComputedStyle(e).opacity") == "1"
        outline = page.locator("textarea").evaluate("e => getComputedStyle(e).outlineStyle")
        assert outline == "none", f"IME preedit must not show a focus rectangle: {outline}"
        cdp.send("Input.insertText", {"text": "中文"})
        page.wait_for_function("fixture.received.join('') === '中文'")
        assert page.locator("textarea").input_value() == ""
        assert page.locator("textarea").evaluate("e => getComputedStyle(e).opacity") == "0"
        page.evaluate("fixture.received.length = 0")
        cdp.send("Input.imeSetComposition", {
            "text": "取消", "selectionStart": 2, "selectionEnd": 2,
        })
        cdp.send("Input.imeSetComposition", {
            "text": "", "selectionStart": 0, "selectionEnd": 0,
        })
        assert page.evaluate("fixture.received.join('')") == ""
        page.evaluate("""() => {
            const data = new DataTransfer();
            data.setData('text/plain', '粘贴 test');
            document.activeElement.dispatchEvent(new ClipboardEvent('paste', {
                clipboardData: data, bubbles: true, cancelable: true,
            }));
        }""")
        assert page.evaluate("fixture.received.join('')") == "粘贴 test"
        browser.close()
    print("PASS: cursor anchoring across rows/columns/fonts, keyboard, IME commit/cancel, paste")


if __name__ == "__main__":
    main()

// Run after make build with Playwright available to Node (or set NODE_PATH).
// Uses an isolated daemon and database; never contacts a deployed service.
const assert = require("node:assert/strict");
const { spawn, execFileSync } = require("node:child_process");
const { mkdtempSync, rmSync } = require("node:fs");
const { tmpdir } = require("node:os");
const path = require("node:path");
const { once } = require("node:events");
const { chromium, expect } = require("@playwright/test");

async function main() {
  const root = path.resolve(__dirname, "../../../..");
  const data = mkdtempSync(path.join(tmpdir(), "tw-clipboard-"));
  const env = { ...process.env, TEAM_WORKER_DATA_DIR: data,
    TEAM_WORKER_SOCKET: path.join(data, "daemon.sock"), TEAM_WORKER_API_KEY: "clipboard-test-key" };
  const daemon = spawn(path.join(root, "bin/team-workerd"), ["--data-dir", data,
    "--socket", env.TEAM_WORKER_SOCKET, "--listen", "127.0.0.1:0", "--board"], { env });
  let browser;
  try {
    // The daemon reports the OS-assigned listener address.
    let logs = "";
    const url = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error("Daemon did not start: " + logs)), 10000);
      daemon.stderr.on("data", (chunk) => {
        logs += chunk;
        const match = logs.match(/127\.0\.0\.1:(\d+)/);
        if (match) { clearTimeout(timer); resolve("http://127.0.0.1:" + match[1]); }
      });
      daemon.on("exit", () => { clearTimeout(timer); reject(new Error(logs)); });
    });
    execFileSync(path.join(root, "bin/team-worker"), ["team", "spawn-team", "demo"], { env });
    browser = await chromium.launch({ headless: true,
      executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE,
      args: ["--no-proxy-server", "--host-resolver-rules=MAP board.example.test 127.0.0.1"] });
    const context = await browser.newContext();
    await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: url });
    const reader = await context.newPage();
    await reader.goto(url);
    const insecure = url.replace("127.0.0.1", "board.example.test");
    for (const mode of ["http", "secure", "denied"]) {
      const page = await context.newPage();
      if (mode === "denied") await page.addInitScript(() => {
        Object.defineProperty(navigator, "clipboard", { value: {
          writeText: () => Promise.reject(new DOMException("Denied", "NotAllowedError")),
        } });
      });
      await page.goto(mode === "http" ? insecure : url);
      if (await page.getByLabel("Access key").isVisible()) {
        await page.getByLabel("Access key").fill(env.TEAM_WORKER_API_KEY);
        await page.getByRole("button", { name: "Sign in", exact: true }).click();
      }
      const button = page.getByRole("button", { name: "Copy demo", exact: true });
      await expect(button).toBeVisible();
      if (mode === "http") assert.equal(await page.evaluate(() => navigator.clipboard), undefined);
      await reader.bringToFront();
      await reader.evaluate(() => navigator.clipboard.writeText("previous value"));
      await page.bringToFront();
      await button.click();
      await expect(button.locator("path").first()).toHaveAttribute("d", "m3 8.5 3 3 7-7");
      await reader.bringToFront();
      const actual = await reader.evaluate(() => navigator.clipboard.readText());
      assert.equal(actual, "demo", `${mode}: clicking copy must update the clipboard`);
      console.log(`PASS: ${mode} team name copied to clipboard`);
      if (mode === "denied") {
        await expect(button.locator("path").first()).not.toHaveAttribute("d", "m3 8.5 3 3 7-7");
        await page.evaluate(() => { document.execCommand = () => false; });
        let message;
        page.once("dialog", async (dialog) => {
          message = dialog.message();
          await dialog.dismiss();
        });
        await page.bringToFront();
        await button.click();
        assert.match(message, /copy/i);
        await expect(button.locator("path").first()).not.toHaveAttribute("d", "m3 8.5 3 3 7-7");
        assert.equal(await page.locator("textarea").count(), 0);
        console.log("PASS: copy failure is reported and temporary input is removed");
      }
      await page.close();
    }
  } finally {
    if (browser) await browser.close();
    daemon.kill();
    await once(daemon, "exit");
    rmSync(data, { recursive: true, force: true });
  }
}
main().catch((error) => { console.error(error); process.exitCode = 1; });

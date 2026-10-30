import type { ExtensionAPI, ExtensionContext } from "@earendil-works/pi-coding-agent";
import { spawn } from "node:child_process";

/**
 * team-worker extension for pi. `team-worker spawn` writes this file and
 * loads it with `pi -e`; the agent's identity comes from the TEAM_WORKER_*
 * variables spawn sets.
 *
 * The extension holds no coordination logic. It forwards pi's lifecycle to
 * `team-worker hook`, the same handlers Claude Code calls:
 * - while the agent is idle, `hook stop` waits for messages, new tasks or
 *   (for the leader) team progress, and its reason starts a new turn;
 * - after tool calls, `hook post-tool-use` steers in messages that arrived;
 * - after a compaction, resume or fork, `hook session-start` restores the
 *   team context for the next turn.
 * Hook failures are silent, so a team-worker problem never blocks pi.
 */

const env = process.env;
const team = env.TEAM_WORKER_TEAM_NAME ?? "";
const agent = env.TEAM_WORKER_AGENT_NAME ?? "";
const role = env.TEAM_WORKER_AGENT_LEADER === "1" ? "leader" : "worker";
const bin = env.TEAM_WORKER_BIN || "team-worker";

/** Seconds one idle `hook stop` call waits before the loop asks again. */
const IDLE_WAIT = 50;

type HookOutput = {
  decision?: string;
  reason?: string;
  hookSpecificOutput?: { additionalContext?: string };
};

function runHook(event: string, input: object, extra: string[] = [], signal?: AbortSignal): Promise<HookOutput | undefined> {
  return new Promise((resolve) => {
    const args = ["hook", event, "--team", team, "--agent", agent, "--role", role, ...extra];
    const child = spawn(bin, args, { stdio: ["pipe", "pipe", "ignore"], signal });
    let out = "";
    child.stdout.on("data", (chunk) => (out += chunk));
    child.on("error", () => resolve(undefined));
    child.on("close", () => {
      try {
        resolve(out.trim() === "" ? undefined : (JSON.parse(out) as HookOutput));
      } catch {
        resolve(undefined);
      }
    });
    child.stdin.on("error", () => {});
    child.stdin.end(JSON.stringify(input));
  });
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener("abort", () => (clearTimeout(timer), resolve()), { once: true });
  });
}

export default function (pi: ExtensionAPI): void {
  if (team === "" || agent === "") return;

  let idleWatch: AbortController | undefined;
  // Wakes that produced no tool call count as nagging; after one, `hook stop`
  // stops pushing open tasks and only reports something new.
  let wakes = 0;
  let toolsSinceWake = 0;

  const stopIdleWatch = () => {
    idleWatch?.abort();
    idleWatch = undefined;
  };

  async function watchWhileIdle(ctx: ExtensionContext): Promise<void> {
    stopIdleWatch();
    // Print and JSON runs exit once settled; only long-lived sessions wait for work.
    if (ctx.mode !== "tui" && ctx.mode !== "rpc") return;
    const watch = new AbortController();
    idleWatch = watch;
    while (!watch.signal.aborted && ctx.isIdle()) {
      const started = Date.now();
      const out = await runHook("stop", { stop_hook_active: wakes > 0 && toolsSinceWake === 0 },
        ["--wait", String(IDLE_WAIT)], watch.signal);
      if (watch.signal.aborted) return;
      if (out?.decision === "block" && out.reason) {
        wakes++;
        toolsSinceWake = 0;
        stopIdleWatch();
        pi.sendUserMessage(out.reason, ctx.isIdle() ? undefined : { deliverAs: "followUp" });
        return;
      }
      // `hook stop` returns at once when there is nothing to wait for, e.g. a
      // leader whose tasks are all done; keep checking messages at a calm pace.
      const left = 5000 - (Date.now() - started);
      if (left > 0) await sleep(left, watch.signal);
    }
  }

  async function restoreContext(source: string): Promise<void> {
    const out = await runHook("session-start", { source });
    const text = out?.hookSpecificOutput?.additionalContext;
    if (text) {
      pi.sendMessage({ customType: "team-worker", content: text, display: false }, { deliverAs: "nextTurn" });
    }
  }

  pi.on("session_start", async (event, ctx) => {
    if (event.reason === "resume" || event.reason === "fork" || event.reason === "new") {
      await restoreContext(event.reason);
    }
    if (ctx.isIdle()) void watchWhileIdle(ctx);
  });

  pi.on("session_compact", async () => {
    await restoreContext("compact");
  });

  pi.on("agent_start", () => {
    stopIdleWatch();
  });

  pi.on("agent_settled", (_event, ctx) => {
    void watchWhileIdle(ctx);
  });

  pi.on("tool_execution_end", async () => {
    toolsSinceWake++;
    const out = await runHook("post-tool-use", {});
    const text = out?.hookSpecificOutput?.additionalContext;
    if (text) pi.sendUserMessage(text, { deliverAs: "steer" });
  });

  pi.on("session_shutdown", () => {
    stopIdleWatch();
  });
}

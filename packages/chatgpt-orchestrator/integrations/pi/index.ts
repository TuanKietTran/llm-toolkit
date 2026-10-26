/**
 * chatgpt-orchestrator bridge for pi.
 *
 * pi has no built-in MCP client (confirmed in its own docs: "It intentionally does not include
 * built-in MCP..."). The `claude-cli` provider extension's `--mcp-config` is a flag it forwards to
 * the separate `claude` binary it shells out to — not a pi-native mechanism, and not something
 * that generalizes to pi's own providers (llamacpp, etc).
 *
 * So instead of registering an mcpServers manifest (there is nothing in pi to register it with),
 * this extension IS a minimal MCP client: on session_start it spawns
 * `uv run orchestrator serve-mcp --transport stdio` as a child process, connects to it with the
 * official MCP TypeScript SDK, lists its tools, and registers one pi tool per MCP tool that
 * forwards calls through. New tools the server adds later show up automatically — nothing here is
 * orchestrator-specific beyond the spawn command, so this works with any pi provider, not just
 * claude-cli.
 */

import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";

// integrations/pi/index.ts -> project root is two levels up.
const EXTENSION_DIR = dirname(fileURLToPath(import.meta.url));
const PROJECT_DIR = process.env.CHATGPT_ORCHESTRATOR_DIR ?? resolve(EXTENSION_DIR, "..", "..");

const TOOL_PREFIX = "chatgpt_";

interface Bridge {
  client: Client;
  transport: StdioClientTransport;
}

let bridge: Bridge | undefined;
let connecting: Promise<Bridge> | undefined;

async function connect(): Promise<Bridge> {
  if (bridge) return bridge;
  if (connecting) return connecting;

  connecting = (async () => {
    // The MCP SDK's stdio transport only inherits a safelisted subset of the environment by
    // default (a deliberate secret-leak guard) unless `env` is passed explicitly. Pass the full
    // parent environment through so shell-set ORCH_* overrides behave the same here as they do
    // for a directly-invoked `uv run orchestrator ...` (config.toml/.env-based settings already
    // work either way, since pydantic-settings reads .env straight off disk).
    const env: Record<string, string> = {};
    for (const [k, v] of Object.entries(process.env)) if (v !== undefined) env[k] = v;

    const transport = new StdioClientTransport({
      command: "uv",
      args: ["run", "orchestrator", "serve-mcp", "--transport", "stdio"],
      cwd: PROJECT_DIR,
      env,
    });
    const client = new Client({ name: "pi-chatgpt-orchestrator-bridge", version: "0.1.0" });
    await client.connect(transport);
    const b = { client, transport };
    bridge = b;
    return b;
  })();

  try {
    return await connecting;
  } finally {
    connecting = undefined;
  }
}

async function disconnect(): Promise<void> {
  const b = bridge;
  bridge = undefined;
  if (!b) return;
  try {
    await b.client.close();
  } catch {
    // best-effort: the child process may already be gone.
  }
}

// An MCP `inputSchema` is already JSON Schema ({type:"object", properties, required}) — the same
// shape TypeBox's Type.Object(...) produces at runtime — so it can be passed straight through as
// a pi tool's `parameters`.
function toPiParameters(inputSchema: unknown): object {
  if (inputSchema && typeof inputSchema === "object") return inputSchema as object;
  return { type: "object", properties: {} };
}

function resultToText(result: { content?: Array<Record<string, unknown>> }): string {
  const blocks = result.content ?? [];
  if (blocks.length === 0) return JSON.stringify(result);
  return blocks
    .map((b) => (b.type === "text" ? String(b.text) : JSON.stringify(b)))
    .join("\n");
}

export default function (pi: ExtensionAPI) {
  pi.on("session_start", async (_event, ctx) => {
    let b: Bridge;
    try {
      b = await connect();
    } catch (err) {
      ctx.ui.notify(
        `chatgpt-orchestrator: failed to start (is 'uv' on PATH and deps synced in ${PROJECT_DIR}?): ${err}`,
        "error",
      );
      return;
    }

    let tools: Awaited<ReturnType<Client["listTools"]>>["tools"];
    try {
      ({ tools } = await b.client.listTools());
    } catch (err) {
      ctx.ui.notify(`chatgpt-orchestrator: failed to list tools: ${err}`, "error");
      return;
    }

    for (const tool of tools) {
      pi.registerTool({
        name: `${TOOL_PREFIX}${tool.name}`,
        label: tool.name,
        description: tool.description ?? `chatgpt-orchestrator tool: ${tool.name}`,
        parameters: toPiParameters(tool.inputSchema),
        async execute(_toolCallId, params) {
          const result = await b.client.callTool({
            name: tool.name,
            arguments: params as Record<string, unknown>,
          });
          return {
            content: [{ type: "text", text: resultToText(result as any) }],
            details: { raw: result },
          };
        },
      });
    }

    ctx.ui.notify(`chatgpt-orchestrator: registered ${tools.length} tool(s)`, "info");
  });

  pi.on("session_shutdown", async () => {
    await disconnect();
  });
}

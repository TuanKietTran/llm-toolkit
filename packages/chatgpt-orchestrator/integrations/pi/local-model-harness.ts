/**
 * Local-model request harness for pi.
 *
 * This deliberately stays at pi's provider boundary. It does not maintain a second
 * conversation or rewrite the session file: the local server owns its KV/prompt cache,
 * while pi owns the durable transcript. The harness makes the two cooperate by:
 *
 *   - requesting prompt reuse from llama.cpp-compatible servers;
 *   - keeping Ollama models loaded between requests;
 *   - applying a fresh output-token budget to every provider request; and
 *   - exposing cache/budget metadata through a stable request header and notifications.
 *
 * It is safe to load globally. Non-local providers are left untouched unless explicitly
 * listed in PI_LOCAL_PROVIDERS.
 */

import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const localProviders = new Set(
  (process.env.PI_LOCAL_PROVIDERS ?? "ollama,llama.cpp,llamacpp,local")
    .split(",")
    .map((value) => value.trim().toLowerCase())
    .filter(Boolean),
);

const configuredMaxTokens = process.env.PI_TURN_MAX_TOKENS;
const cacheHeader = process.env.PI_CACHE_HEADER ?? "x-pi-prompt-cache-key";
// If unset, Ollama uses its own default keep-alive policy. This avoids making the extension
// responsible for model residency; set it only when a long-lived warm model is desired.
const keepAlive = process.env.PI_OLLAMA_KEEP_ALIVE;
let cacheEnabled = process.env.PI_LOCAL_PROMPT_CACHE !== "0";
const configuredEffort = process.env.PI_MODEL_EFFORT?.trim().toLowerCase();

let currentTurn = 0;
let requestCount = 0;
let budgetOverride: number | undefined;
let effortOverride: string | undefined;
let keepAliveOverride: string | null | undefined;

function positiveInt(value: string | undefined, fallback: number): number {
  const parsed = Number.parseInt(value ?? "", 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : fallback;
}

function providerName(ctx: any): string {
  return String(ctx.model?.provider ?? "").toLowerCase();
}

function modelName(ctx: any): string {
  return String(ctx.model?.id ?? "local");
}

function isLocal(ctx: any): boolean {
  return localProviders.has(providerName(ctx));
}

/** Keep the cache namespace stable across turns; the output budget is reset separately. */
function cacheKey(ctx: any): string {
  const sessionId = String(ctx.sessionManager?.getSessionId?.() ?? "session");
  return `pi:${sessionId}:${providerName(ctx)}:${modelName(ctx)}`;
}

function maxTokens(ctx: any): number {
  const configured = budgetOverride ?? positiveInt(configuredMaxTokens, 0);
  if (configured > 0) return configured;
  const modelLimit = Number(ctx.model?.maxTokens);
  return Number.isFinite(modelLimit) && modelLimit > 0 ? modelLimit : 4096;
}

function effort(ctx: any): string {
  return effortOverride || configuredEffort || String(ctx.thinkingLevel ?? "off").toLowerCase();
}

function effectiveKeepAlive(): string | undefined {
  return keepAliveOverride === null ? undefined : (keepAliveOverride ?? keepAlive);
}

function notify(ctx: any, message: string, level: "info" | "warning" | "error" = "info"): void {
  if (ctx.hasUI) ctx.ui.notify(message, level);
}

function setFreshBudget(payload: Record<string, any>, budget: number): void {
  // Pi uses max_tokens for OpenAI-compatible local servers. Keep the alternate spelling
  // useful for proxies that translate OpenAI Responses requests.
  if ("max_completion_tokens" in payload) payload.max_completion_tokens = budget;
  else payload.max_tokens = budget;
  if ("max_output_tokens" in payload) payload.max_output_tokens = budget;
}

export default function (pi: ExtensionAPI) {
  pi.on("turn_start", (event) => {
    currentTurn = Number(event.turnIndex ?? currentTurn + 1);
    requestCount = 0;
  });

  pi.registerCommand("local-harness", {
    description: "Show local-model token, cache, keep-alive, and effort settings",
    handler: async (_args, ctx) => {
      notify(
        ctx,
        `local harness: budget=${maxTokens(ctx)}, effort=${effort(ctx)}, keep_alive=${effectiveKeepAlive() ?? "default/off"}, cache=${cacheEnabled ? "on" : "off"}`,
      );
    },
  });

  pi.registerCommand("local-budget", {
    description: "Set per-request output tokens (usage: /local-budget 2048|auto)",
    handler: async (args, ctx) => {
      const value = args.trim().toLowerCase();
      if (value === "auto" || value === "default" || value === "") {
        budgetOverride = undefined;
        notify(ctx, `local harness: budget reset to ${maxTokens(ctx)} (model/default)`);
        return;
      }
      const parsed = positiveInt(value, 0);
      if (!parsed) {
        notify(ctx, "Usage: /local-budget 2048|auto", "warning");
        return;
      }
      budgetOverride = parsed;
      notify(ctx, `local harness: budget=${parsed} output tokens/request`);
    },
  });

  pi.registerCommand("local-effort", {
    description: "Set model effort (usage: /local-effort off|low|medium|high|auto)",
    handler: async (args, ctx) => {
      const value = args.trim().toLowerCase();
      const allowed = new Set(["off", "minimal", "low", "medium", "high", "xhigh"]);
      if (value === "auto" || value === "default" || value === "") {
        effortOverride = undefined;
        notify(ctx, `local harness: effort=${effort(ctx)} (Pi/model default)`);
        return;
      }
      if (!allowed.has(value)) {
        notify(ctx, "Usage: /local-effort off|minimal|low|medium|high|xhigh|auto", "warning");
        return;
      }
      effortOverride = value;
      pi.setThinkingLevel(value as any);
      notify(ctx, `local harness: effort=${value}`);
    },
  });

  pi.registerCommand("local-keepalive", {
    description: "Set Ollama keep-alive (usage: /local-keepalive 30m|off|default)",
    handler: async (args, ctx) => {
      const value = args.trim().toLowerCase();
      if (value === "default" || value === "") {
        keepAliveOverride = undefined;
        notify(ctx, `local harness: keep_alive=${effectiveKeepAlive() ?? "default/off"}`);
        return;
      }
      if (value === "off" || value === "none") {
        keepAliveOverride = null;
        notify(ctx, "local harness: keep_alive disabled");
        return;
      }
      if (!/^[0-9]+(s|m|h)$/.test(value)) {
        notify(ctx, "Usage: /local-keepalive 30m|off|default", "warning");
        return;
      }
      keepAliveOverride = value;
      notify(ctx, `local harness: keep_alive=${value}`);
    },
  });

  pi.registerCommand("local-cache", {
    description: "Enable or disable local prompt/KV cache hints (usage: /local-cache on|off)",
    handler: async (args, ctx) => {
      const value = args.trim().toLowerCase();
      if (value === "on" || value === "1") cacheEnabled = true;
      else if (value === "off" || value === "0") cacheEnabled = false;
      else {
        notify(ctx, "Usage: /local-cache on|off", "warning");
        return;
      }
      notify(ctx, `local harness: cache=${cacheEnabled ? "on" : "off"}`);
    },
  });

  pi.on("before_provider_headers", (event, ctx) => {
    if (!isLocal(ctx)) return;
    const budget = maxTokens(ctx);
    event.headers[cacheHeader] = cacheKey(ctx);
    event.headers["x-pi-turn"] = String(currentTurn);
    event.headers["x-pi-turn-max-tokens"] = String(budget);
    event.headers["x-pi-model-effort"] = effort(ctx);
    event.headers["x-pi-session-id"] = ctx.sessionManager.getSessionId();
  });

  pi.on("before_provider_request", (event, ctx) => {
    if (!isLocal(ctx)) return;
    const payload = { ...(event.payload as Record<string, any>) };
    const level = effort(ctx);
    setFreshBudget(payload, maxTokens(ctx));
    requestCount += 1;

    // Pi already serializes thinking for providers that declare a thinkingLevelMap. For
    // local OpenAI-compatible servers, fill the gap without replacing an existing mapping.
    if (!("reasoning_effort" in payload) && level) payload.reasoning_effort = level;

    if (cacheEnabled) {
      // llama.cpp's OpenAI-compatible server understands cache_prompt. Unknown fields are
      // intentionally not sent to Ollama because its OpenAI shim rejects some extensions.
      if (providerName(ctx) === "llama.cpp" || providerName(ctx) === "llamacpp") {
        payload.cache_prompt = true;
      }
      if (providerName(ctx) === "ollama" && effectiveKeepAlive()) {
        payload.keep_alive = effectiveKeepAlive();
      }
      if (providerName(ctx) === "ollama" && configuredEffort) {
        payload.think = level !== "off" && level !== "none";
      }
    }

    return payload;
  });

  pi.on("agent_settled", (_event, ctx) => {
    if (requestCount > 0) {
      ctx.ui.notify(
        `local harness: turn ${currentTurn}, ${requestCount} request(s), max ${maxTokens(ctx)} output tokens/request, effort ${effort(ctx)}`,
        "info",
      );
    }
  });
}

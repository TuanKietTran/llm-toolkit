# llm-toolkit

Tools for routing and orchestrating LLMs. Each package is self-contained; see its own README.

| Package | What it is |
|---|---|
| [`packages/gorouter`](packages/gorouter) | Self-hosted LLM router in Go: OpenAI and Anthropic endpoints, fallback combos, OAuth, quotas, dashboard |
| [`packages/chatgpt-orchestrator`](packages/chatgpt-orchestrator) | Claude as the orchestration brain, driving a pool of browser-based ChatGPT contexts as cheap workers, exposed over MCP |

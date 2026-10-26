"""Example inward MCP server: one safe `fetch_url` tool a ChatGPT worker can be equipped with.

Run standalone to see it start over stdio:

    uv run python examples/example_mcp_server.py

Register it so workers can use it (see .env.example's ORCH_INWARD_MCP_SERVERS):

    ORCH_INWARD_MCP_SERVERS='[{"name": "example-tools", "transport": "stdio",
      "command": "uv", "args": ["run", "python", "examples/example_mcp_server.py"]}]'

Then spawn a worker with equip_tools=True (via the `ask` CLI's --equip-tools, or the MCP
`spawn_worker(equip_tools=true)` tool) and it can call fetch_url through its ReAct loop
(:mod:`orchestrator.workers.protocol`). This is a template, not a general-purpose tool suite —
keep it tiny and add your own tools the same way.
"""

from __future__ import annotations

import httpx
from mcp.server.fastmcp import FastMCP

MAX_CHARS = 20_000
TIMEOUT_S = 10.0

mcp = FastMCP("example-tools")


@mcp.tool()
async def fetch_url(url: str) -> str:
    """Fetch a URL over HTTP(S) and return up to ~20KB of its text body.

    Only http:// and https:// are allowed. The response is truncated, not streamed — this is a
    minimal example tool, not a general-purpose scraper.
    """
    if not (url.startswith("http://") or url.startswith("https://")):
        raise ValueError("fetch_url only supports http:// and https:// URLs")
    async with httpx.AsyncClient(timeout=TIMEOUT_S, follow_redirects=True) as client:
        resp = await client.get(url)
        resp.raise_for_status()
        body = resp.text
    if len(body) > MAX_CHARS:
        body = body[:MAX_CHARS] + f"\n...[truncated, {len(body)} chars total]"
    return body


if __name__ == "__main__":
    mcp.run(transport="stdio")

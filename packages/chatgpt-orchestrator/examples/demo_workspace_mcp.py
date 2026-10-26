"""Bounded coding and Playwright tools for worker-assisted demo development.

This intentionally is not a general shell/filesystem server. Every path is confined to
``/home/kyle/demo-plan`` and browser execution is an allowlisted Playwright command.
"""

from __future__ import annotations

import asyncio
import os
from pathlib import Path

from mcp.server.fastmcp import FastMCP


ROOT = Path(os.getenv("DEMO_WORKSPACE_ROOT", "/home/kyle/demo-plan")).resolve()
MAX_READ = 80_000
MAX_WRITE = 200_000
MAX_OUTPUT = 40_000
ALLOWED_PROJECTS = {
    "auris-desktop",
    "contoso-desktop",
    "northwind-desktop",
    "auris-mobile",
    "contoso-mobile",
    "northwind-mobile",
}

mcp = FastMCP("demo-workspace")


def _path(relative: str) -> Path:
    candidate = (ROOT / relative).resolve()
    try:
        candidate.relative_to(ROOT)
    except ValueError as exc:
        raise ValueError("path must stay inside the demo workspace") from exc
    if candidate.is_symlink():
        raise ValueError("symlink targets are not accepted")
    return candidate


async def _run(
    *args: str,
    cwd: Path,
    timeout: float = 180.0,
    env: dict[str, str] | None = None,
) -> dict:
    process = await asyncio.create_subprocess_exec(
        *args,
        cwd=str(cwd),
        env={**os.environ, **(env or {})},
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.STDOUT,
    )
    try:
        output, _ = await asyncio.wait_for(process.communicate(), timeout=timeout)
    except TimeoutError:
        process.terminate()
        await process.wait()
        raise RuntimeError(f"command exceeded {timeout:g} seconds")
    text = output.decode("utf-8", errors="replace")
    if len(text) > MAX_OUTPUT:
        text = text[-MAX_OUTPUT:]
    return {"exit_code": process.returncode, "output": text}


@mcp.tool()
def list_files(prefix: str = "") -> list[str]:
    """List files below a workspace-relative directory."""
    root = _path(prefix)
    if not root.exists():
        return []
    return [
        str(path.relative_to(ROOT))
        for path in sorted(root.rglob("*"))
        if path.is_file() and "node_modules" not in path.parts
    ][:5000]


@mcp.tool()
def read_file(path: str) -> str:
    """Read a UTF-8 workspace file, capped at 80 KB."""
    target = _path(path)
    if not target.is_file():
        raise FileNotFoundError(path)
    return target.read_text(encoding="utf-8", errors="replace")[:MAX_READ]


@mcp.tool()
def write_file(path: str, content: str) -> dict:
    """Create or replace one UTF-8 file inside the demo workspace."""
    if len(content.encode("utf-8")) > MAX_WRITE:
        raise ValueError("content exceeds 200 KB")
    target = _path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_name(f".{target.name}.worker-tmp")
    temporary.write_text(content, encoding="utf-8")
    os.replace(temporary, target)
    return {"path": str(target.relative_to(ROOT)), "bytes": target.stat().st_size}


@mcp.tool()
async def validate_demo_data() -> dict:
    """Run the deterministic corpus/staff/feature validator without regenerating data."""
    return await _run(
        "/home/kyle/odysseus_fork/.venv/bin/python",
        str(ROOT / "tools/generate_demo_platforms.py"),
        "--validate-only",
        cwd=ROOT,
    )


@mcp.tool()
async def run_playwright(project: str, test_file: str = "") -> dict:
    """Run one allowlisted headless Playwright project, optionally one tests/*.spec.mjs file."""
    if project not in ALLOWED_PROJECTS:
        raise ValueError(f"unsupported project; choose one of {sorted(ALLOWED_PROJECTS)}")
    args = ["npx", "playwright", "test", "--project", project]
    if test_file:
        if "/" in test_file or not test_file.endswith(".spec.mjs"):
            raise ValueError("test_file must be a basename ending in .spec.mjs")
        args.append(f"tests/{test_file}")
    platform = project.split("-", 1)[0]
    return await _run(
        *args,
        cwd=ROOT / "e2e",
        timeout=300.0,
        env={"DEMO_PLATFORM": platform},
    )


if __name__ == "__main__":
    mcp.run(transport="stdio")

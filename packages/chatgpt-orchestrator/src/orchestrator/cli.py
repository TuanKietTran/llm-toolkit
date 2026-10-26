"""orchestrator CLI: serve-mcp, serve-acp, ask (guarded live smoke), accounts, doctor."""

from __future__ import annotations

import asyncio
from typing import Callable, TypeVar

import typer

from .config import get_settings
from .telemetry import configure_logging, get_logger

app = typer.Typer(add_completion=False, no_args_is_help=True)
accounts_app = typer.Typer(add_completion=False, no_args_is_help=True)
app.add_typer(accounts_app, name="accounts", help="Manage the account fleet.")

log = get_logger(__name__)

_TOS_REMINDER = (
    "Reminder: this drives the free ChatGPT web UI via browser automation, which is against "
    "OpenAI's usage policies; logged-in accounts risk suspension. Proceed at your own risk."
)

T = TypeVar("T")


@app.callback()
def _main() -> None:
    """chatgpt-orchestrator: an MCP tool server fronting a pool of browser-driven ChatGPT workers."""
    settings = get_settings()
    configure_logging(settings.log_level, json_logs=settings.log_json)


@app.command("serve-mcp")
def serve_mcp(
    transport: str = typer.Option("stdio", help="stdio (local agents) or http (remote)"),
    host: str = typer.Option("127.0.0.1", help="Bind host for --transport http"),
    port: int = typer.Option(8000, help="Bind port for --transport http"),
) -> None:
    """Serve the outward MCP tool server. stdio mode never writes to stdout except protocol frames."""
    if transport not in ("stdio", "http"):
        typer.echo(f"invalid --transport {transport!r}; must be 'stdio' or 'http'", err=True)
        raise typer.Exit(2)

    from .server import OrchestratorCore, run_http, run_stdio

    settings = get_settings()
    core = OrchestratorCore(settings)
    if transport == "stdio":
        asyncio.run(run_stdio(core))
    else:
        asyncio.run(run_http(core, host=host, port=port))


@app.command("serve-acp")
def serve_acp() -> None:
    """Serve the ACP (Agent Client Protocol) adapter over stdio, for ACP-speaking editors."""
    from .server import OrchestratorCore
    from .server.acp_agent import run_acp_stdio

    settings = get_settings()
    core = OrchestratorCore(settings)
    asyncio.run(run_acp_stdio(core))


@app.command()
def ask(
    prompt: str,
    equip_tools: bool = typer.Option(
        False, "--equip-tools", help="Equip the worker with the configured inward MCP tools"
    ),
    open_attempts: int = typer.Option(
        2,
        "--open-attempts",
        min=1,
        help="Retry pre-submit worker-open failures on another configured identity",
    ),
) -> None:
    """One-shot: spawn a worker, ask it PROMPT, print the reply, close it. Hits real ChatGPT."""
    typer.echo(_TOS_REMINDER, err=True)

    from .server import OrchestratorCore
    from .server.core import WorkerNotFound
    from .chatgpt.exceptions import ChatGPTError

    async def run() -> str:
        settings = get_settings()
        async with OrchestratorCore(settings) as core:
            last_open_error: ChatGPTError | None = None
            for _ in range(open_attempts):
                try:
                    wid = await core.spawn_worker(equip_tools=equip_tools)
                except ChatGPTError as exc:
                    # No prompt was submitted, so trying another identity cannot duplicate work.
                    last_open_error = exc
                    continue
                try:
                    return await core.ask(wid, prompt)
                finally:
                    try:
                        await core.close_worker(wid)
                    except WorkerNotFound:
                        # A failed ask quarantines and removes its worker inside the core.
                        pass
            assert last_open_error is not None
            raise last_open_error

    reply = asyncio.run(run())
    typer.echo(reply)


@accounts_app.command("list")
def accounts_list() -> None:
    """Print the current account fleet's status as a table."""
    from .accounts.manager import AccountManager

    settings = get_settings()
    report = AccountManager(settings).report()

    typer.echo(
        f"{'id':<16}{'mode':<13}{'inflight':<9}{'budget':<10}{'cooldown_s':<11}"
        f"{'sched':<7}{'strikes':<8}{'dead'}"
    )
    for r in report["accounts"]:
        budget = f"{r['budget_used']}/{r['budget_limit']}"
        typer.echo(
            f"{r['id']:<16}{r['mode']:<13}{r['inflight']:<9}{budget:<10}"
            f"{r['cooldown_remaining_s']:<11}{str(r['schedulable']):<7}{r['strikes']:<8}{r['dead']}"
        )
    typer.echo(f"\ncapacity: {report['capacity']}")


@accounts_app.command("alloc-email")
def accounts_alloc_email() -> None:
    """Mint one masked email alias via the configured provider (sanity-checks the token)."""
    from .accounts.email_provider import build_email_provider

    settings = get_settings()

    async def run() -> str:
        provider = build_email_provider(settings.email)
        try:
            return await provider.allocate()
        finally:
            aclose = getattr(provider, "aclose", None)
            if aclose is not None:
                await aclose()

    typer.echo(asyncio.run(run()))


@accounts_app.command("ddg-login")
def accounts_ddg_login(
    username: str = typer.Option(..., "--username", help="DuckDuckGo Email Protection username"),
) -> None:
    """Request a DDG login OTP, prompt for it, and print the resulting access token.

    Never written to disk here — copy the printed token into .env as
    ORCH_EMAIL__DDG_ACCESS_TOKEN yourself.
    """
    import httpx

    from .accounts.email_provider import DuckDuckGoEmailProvider

    async def run() -> str:
        async with httpx.AsyncClient(timeout=20.0) as client:
            await DuckDuckGoEmailProvider.request_login_otp(username, client=client)
            typer.echo(f"OTP sent to {username}'s Email Protection inbox.")
            otp = typer.prompt("Enter the OTP")
            return await DuckDuckGoEmailProvider.login(username, otp, client=client)

    token = asyncio.run(run())
    typer.echo("\nAccess token (put in .env as ORCH_EMAIL__DDG_ACCESS_TOKEN; do not commit):")
    typer.echo(token)


@app.command()
def doctor() -> None:
    """Run environment checks; PASS/FAIL each, non-zero exit if anything fails."""
    ok = True
    settings_holder: dict = {}

    def check(name: str, fn: Callable[[], T]) -> None:
        nonlocal ok
        try:
            detail = fn()
            typer.echo(f"[PASS] {name}" + (f" — {detail}" if detail else ""))
        except Exception as exc:  # noqa: BLE001 - a doctor check must never crash the run
            ok = False
            typer.echo(f"[FAIL] {name}: {exc}")

    def _settings() -> str:
        s = get_settings()
        settings_holder["settings"] = s
        return f"{len(s.accounts)} account(s) configured"

    check("settings load", _settings)
    s = settings_holder.get("settings")

    def _vpn() -> str:
        from .network import verify_vpn

        return verify_vpn((s or get_settings()).network)

    check("VPN guard", _vpn)

    def _driver_import() -> str:
        driver = (s or get_settings()).browser.driver
        if driver == "patchright":
            import patchright.async_api  # noqa: F401

            return "patchright import OK"
        import playwright.async_api  # noqa: F401

        return "playwright import OK"

    check("driver import", _driver_import)

    async def _browser_launch() -> str:
        from .browser.manager import BrowserManager
        from .browser.session import SessionSpec

        mgr = BrowserManager(s or get_settings())
        await mgr.start()
        try:
            session = await mgr.new_session(SessionSpec(account_id="doctor", mode="anonymous"))
            await session.close()
        finally:
            await mgr.close()
        return "launched, opened a context, closed"

    check("browser launch", lambda: asyncio.run(_browser_launch()))

    def _mcp_build() -> str:
        from .server import OrchestratorCore, build_server

        core = OrchestratorCore(s or get_settings())
        build_server(core)
        return "server builds"

    check("mcp server build", _mcp_build)

    if not ok:
        raise typer.Exit(1)


__all__ = ["app"]


if __name__ == "__main__":
    app()

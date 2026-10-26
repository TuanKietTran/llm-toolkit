"""A Worker = one browser context + one account + a ChatGPT driver (+ optional inward tools).

For most flows the connected agent (Claude Code / pi) reasons and the worker just answers a prompt
via :meth:`ask`. If the worker is *equipped* with an :class:`InwardToolHost`, :meth:`ask` runs the
worker-side ReAct loop instead. Acquisition/release of the context+account is the orchestrator
core's job; a Worker just binds them and tracks messages for budgeting.
"""

from __future__ import annotations

import asyncio

from ..accounts.manager import AccountState
from ..browser.context_pool import PooledContext
from ..chatgpt.driver import ChatGPTDriver
from ..chatgpt.exceptions import OperationTimeout
from ..config import Settings
from ..telemetry import get_logger, get_metrics
from .inward_mcp import InwardToolHost
from .protocol import run_react

log = get_logger(__name__)
metrics = get_metrics()


class Worker:
    def __init__(
        self,
        worker_id: str,
        ctx: PooledContext,
        account: AccountState,
        driver: ChatGPTDriver,
        settings: Settings,
        *,
        tool_host: InwardToolHost | None = None,
    ) -> None:
        self.id = worker_id
        self.ctx = ctx
        self.account = account
        self.driver = driver
        self.settings = settings
        self.tool_host = tool_host
        self.messages_sent = 0
        self.history: list[tuple[str, str]] = []
        self._opened = False

    @property
    def account_id(self) -> str:
        return self.account.id

    @property
    def equipped(self) -> bool:
        return self.tool_host is not None and bool(self.tool_host.catalog())

    async def open(self) -> None:
        """Connect inward tools (if any) and navigate the driver to a usable composer."""
        if self._opened:
            return
        if self.tool_host is not None:
            await self.tool_host.start()
        await self.driver.open()
        self._opened = True
        log.info("worker_opened", worker=self.id, account=self.account_id, equipped=self.equipped)

    async def send(self, text: str) -> str:
        """Low-level single message on the persistent conversation; consumes one budget unit."""
        timeout = self.settings.agent.ask_timeout_seconds
        try:
            async with asyncio.timeout(timeout):
                reply = await self.driver.ask(text, timeout=timeout)
        except TimeoutError as exc:
            raise OperationTimeout(
                f"complete ask operation exceeded {timeout:g} seconds"
            ) from exc
        self.messages_sent += 1
        self.account.budget.consume(1)
        self.history.append(("user", text))
        self.history.append(("assistant", reply))
        metrics.incr("worker_messages")
        return reply

    async def ask(self, prompt: str) -> str:
        """Answer a prompt (runs the ReAct loop if the worker is equipped with tools)."""
        if self.equipped:
            return await run_react(self, prompt, self.settings.agent)
        return await self.send(prompt)

    async def converse(self, message: str) -> str:
        """Continue the conversation on this same context (multi-turn)."""
        return await self.send(message)

    async def read_context(self, *, include_screenshot: bool = False) -> dict:
        """Inspect this worker's current page: url, visible text, optional screenshot."""
        return await self.driver.read_context(include_screenshot=include_screenshot)

    async def close(self) -> None:
        if self.tool_host is not None:
            await self.tool_host.close()
        log.info("worker_closed", worker=self.id, messages=self.messages_sent)


__all__ = ["Worker"]

"""structlog configuration.

Logs are written to **stderr**, never stdout: when the orchestrator runs as an MCP server over
stdio, stdout is the JSON-RPC protocol channel and must stay clean.
"""

from __future__ import annotations

import logging
import sys

import structlog

_configured = False


class _LiveStderr:
    """Resolves ``sys.stderr`` fresh on every write instead of snapshotting it.

    structlog's ``PrintLogger`` is cached (``cache_logger_on_first_use``) and holds whatever file
    object it was built with for the rest of the process. If something temporarily swaps
    ``sys.stderr`` while that happens (e.g. Click's ``CliRunner`` in tests, ``redirect_stderr``),
    a snapshot would pin a reference to a stream that later gets closed, breaking every subsequent
    log call. Looking it up per-write keeps logging working no matter what wraps stderr later.
    """

    def write(self, data: str) -> int:
        return sys.stderr.write(data)

    def flush(self) -> None:
        sys.stderr.flush()

    def isatty(self) -> bool:
        return sys.stderr.isatty()


def configure_logging(level: str = "INFO", *, json_logs: bool = False) -> None:
    """Configure structlog once. Safe to call repeatedly; later calls just re-apply the level."""
    global _configured

    level_no = logging.getLevelName(level.upper())
    if not isinstance(level_no, int):
        level_no = logging.INFO

    renderer: structlog.types.Processor
    if json_logs:
        renderer = structlog.processors.JSONRenderer()
    else:
        renderer = structlog.dev.ConsoleRenderer(colors=sys.stderr.isatty())

    structlog.configure(
        processors=[
            structlog.contextvars.merge_contextvars,
            structlog.processors.add_log_level,
            structlog.processors.StackInfoRenderer(),
            structlog.processors.format_exc_info,
            structlog.processors.TimeStamper(fmt="iso", utc=True),
            renderer,
        ],
        wrapper_class=structlog.make_filtering_bound_logger(level_no),
        logger_factory=structlog.PrintLoggerFactory(file=_LiveStderr()),
        cache_logger_on_first_use=True,
    )
    _configured = True


def get_logger(name: str | None = None):
    """Return a bound logger, configuring logging with defaults on first use."""
    if not _configured:
        configure_logging()
    return structlog.get_logger(name)


__all__ = ["configure_logging", "get_logger"]

"""Browser layer: one shared process, isolated per-identity sessions, stealth, pool, autoscaler."""

from .autoscaler import Autoscaler, TickResult
from .context_pool import Backpressure, ContextPool, PoolClosed, PooledContext
from .manager import BrowserLaunchError, BrowserManager
from .session import BrowserSession, Fingerprint, SessionSpec, build_context_kwargs, parse_proxy
from .stealth import (
    build_stealth_init_script,
    generate_fingerprint,
    human_mouse_move,
    human_type,
    think,
)

__all__ = [
    "Autoscaler",
    "Backpressure",
    "BrowserLaunchError",
    "BrowserManager",
    "BrowserSession",
    "ContextPool",
    "Fingerprint",
    "PoolClosed",
    "PooledContext",
    "SessionSpec",
    "TickResult",
    "build_context_kwargs",
    "build_stealth_init_script",
    "generate_fingerprint",
    "human_mouse_move",
    "human_type",
    "parse_proxy",
    "think",
]

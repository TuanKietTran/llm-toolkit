"""Typed shapes for MCP tool results (FastMCP infers tool *inputs* from function signatures; these
are for return values so agents get structured, documented output instead of bare dicts)."""

from __future__ import annotations

from pydantic import BaseModel


class AskResult(BaseModel):
    worker_id: str
    reply: str


class BatchItem(BaseModel):
    ok: bool
    reply: str | None = None
    error: str | None = None


class WorkerInfo(BaseModel):
    id: str
    account_id: str
    messages_sent: int
    equipped: bool


class PoolStatus(BaseModel):
    idle: int
    busy: int
    quarantined: int
    active: int
    provisioning: int
    pending: int
    max_contexts: int
    account_capacity: int | None
    mem_available_mb: float
    ema_pending: float
    workers: int


class AccountsStatus(BaseModel):
    capacity: int
    accounts: list[dict]
    as_of: float


__all__ = [
    "AccountsStatus",
    "AskResult",
    "BatchItem",
    "PoolStatus",
    "WorkerInfo",
]

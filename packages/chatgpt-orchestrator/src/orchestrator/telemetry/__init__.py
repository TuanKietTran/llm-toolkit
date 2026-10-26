"""Telemetry: structured logging (to stderr), in-process metrics, and failure snapshots."""

from .logging import configure_logging, get_logger
from .metrics import Metrics, get_metrics
from .snapshots import save_failure_snapshot

__all__ = [
    "Metrics",
    "configure_logging",
    "get_logger",
    "get_metrics",
    "save_failure_snapshot",
]

"""Failure snapshots.

When the ChatGPT driver hits an unexpected DOM state, Cloudflare challenge, or timeout, it dumps
the page HTML + a screenshot + metadata into a timestamped folder so brittle-selector breakage can
be diagnosed offline. Snapshots are git-ignored (they may contain session-identifying content).
"""

from __future__ import annotations

import json
import re
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from .logging import get_logger

log = get_logger(__name__)

_SLUG = re.compile(r"[^a-z0-9]+")


def _slug(text: str) -> str:
    return _SLUG.sub("-", text.lower()).strip("-") or "snapshot"


def save_failure_snapshot(
    reason: str,
    *,
    root: str | Path = "snapshots",
    html: str | None = None,
    screenshot: bytes | None = None,
    url: str | None = None,
    meta: dict[str, Any] | None = None,
) -> Path | None:
    """Write a failure snapshot; return its directory, or ``None`` if writing failed.

    Never raises — snapshotting must not mask the original error.
    """
    try:
        ts = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S_%f")
        out = Path(root) / f"{ts}-{_slug(reason)}"
        out.mkdir(parents=True, exist_ok=True)

        payload: dict[str, Any] = {
            "reason": reason,
            "url": url,
            "captured_at": datetime.now(timezone.utc).isoformat(),
        }
        if meta:
            payload["meta"] = meta
        (out / "meta.json").write_text(json.dumps(payload, indent=2, default=str))

        if html is not None:
            (out / "page.html").write_text(html, encoding="utf-8")
        if screenshot is not None:
            (out / "screenshot.png").write_bytes(screenshot)

        log.warning("failure_snapshot_saved", reason=reason, path=str(out), url=url)
        return out
    except Exception as exc:  # noqa: BLE001 - telemetry must never crash the caller
        log.error("failure_snapshot_error", reason=reason, error=str(exc))
        return None


__all__ = ["save_failure_snapshot"]

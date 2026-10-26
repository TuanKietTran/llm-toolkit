"""Secure-ish persistence for account identities.

Holds (a) the on-disk location of each account's ``storage_state`` (cookies/localStorage from a
one-time login, so identities age instead of starting fresh) and (b) a small JSON registry of
*provisioned* accounts (id, masked email alias, proxy, timestamps) so accounts created at runtime
survive restarts.

The whole directory is git-ignored. Secrets (proxy creds, tokens) live in env/.env, not here;
storage_state files themselves are session-bearing and must never be committed.
"""

from __future__ import annotations

import json
import time
from pathlib import Path
from typing import Any


class AccountStore:
    """Filesystem-backed store for storage_state files and a provisioned-account registry."""

    def __init__(self, root: str | Path = "storage_state") -> None:
        self.root = Path(root)
        self._registry_path = self.root / "registry.json"

    def ensure(self) -> None:
        self.root.mkdir(parents=True, exist_ok=True)

    # ---- storage_state paths ---------------------------------------------------------

    def storage_state_path(self, account_id: str) -> str:
        return str(self.root / f"{account_id}.storage_state.json")

    def has_storage_state(self, account_id: str) -> bool:
        return Path(self.storage_state_path(account_id)).exists()

    # ---- provisioned-account registry ------------------------------------------------

    def load_registry(self) -> list[dict[str, Any]]:
        if not self._registry_path.exists():
            return []
        try:
            return json.loads(self._registry_path.read_text())
        except (json.JSONDecodeError, OSError):
            return []

    def _write_registry(self, entries: list[dict[str, Any]]) -> None:
        self.ensure()
        tmp = self._registry_path.with_suffix(".json.tmp")
        tmp.write_text(json.dumps(entries, indent=2, default=str))
        tmp.replace(self._registry_path)

    def upsert(self, entry: dict[str, Any]) -> None:
        """Insert or replace a provisioned account by ``id``."""
        entries = self.load_registry()
        entry = {**entry, "updated_at": time.time()}
        entry.setdefault("created_at", entry["updated_at"])
        for i, e in enumerate(entries):
            if e.get("id") == entry.get("id"):
                entry["created_at"] = e.get("created_at", entry["created_at"])
                entries[i] = entry
                break
        else:
            entries.append(entry)
        self._write_registry(entries)

    def remove(self, account_id: str) -> None:
        entries = [e for e in self.load_registry() if e.get("id") != account_id]
        self._write_registry(entries)


__all__ = ["AccountStore"]

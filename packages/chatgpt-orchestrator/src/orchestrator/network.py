"""Fail-closed checks for an operator-managed host VPN."""

from __future__ import annotations

import psutil

from .config import NetworkSettings


class VpnUnavailable(RuntimeError):
    """Raised when VPN-required mode cannot verify an active interface."""


def verify_vpn(settings: NetworkSettings) -> str:
    """Verify the configured VPN interface without changing host networking."""
    if not settings.vpn_required:
        return "not required"

    name = settings.vpn_interface.strip()
    stats = psutil.net_if_stats()
    interface = stats.get(name)
    if interface is None:
        available = ", ".join(sorted(stats)) or "none"
        raise VpnUnavailable(f"VPN interface {name!r} not found; available: {available}")
    if not interface.isup:
        raise VpnUnavailable(f"VPN interface {name!r} exists but is down")
    return f"required interface {name!r} is up"


__all__ = ["VpnUnavailable", "verify_vpn"]

from __future__ import annotations

from types import SimpleNamespace

import pytest

from orchestrator.config import NetworkSettings
from orchestrator.network import VpnUnavailable, verify_vpn


def test_vpn_not_required() -> None:
    assert verify_vpn(NetworkSettings()) == "not required"


def test_vpn_required_needs_interface_name() -> None:
    with pytest.raises(ValueError, match="vpn_interface"):
        NetworkSettings(vpn_required=True)


def test_vpn_required_accepts_up_interface(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.setattr(
        "orchestrator.network.psutil.net_if_stats",
        lambda: {"wg0": SimpleNamespace(isup=True)},
    )
    assert "wg0" in verify_vpn(NetworkSettings(vpn_required=True, vpn_interface="wg0"))


def test_vpn_required_rejects_missing_or_down_interface(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.setattr(
        "orchestrator.network.psutil.net_if_stats",
        lambda: {"wg0": SimpleNamespace(isup=False)},
    )
    with pytest.raises(VpnUnavailable, match="down"):
        verify_vpn(NetworkSettings(vpn_required=True, vpn_interface="wg0"))
    with pytest.raises(VpnUnavailable, match="not found"):
        verify_vpn(NetworkSettings(vpn_required=True, vpn_interface="tun0"))

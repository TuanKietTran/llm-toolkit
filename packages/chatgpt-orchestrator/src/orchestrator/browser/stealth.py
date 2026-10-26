"""Anti-detection helpers: fingerprint generation, init-script patches, human-like input.

This complements — does not replace — a stealth-patched driver (Patchright/Camoufox). Those hide
``navigator.webdriver`` and CDP/``Runtime.enable`` leaks; here we (a) generate an internally
consistent fingerprint, (b) inject a small init script so JS-visible device props match it, and
(c) pace typing/mouse/think-time like a human. See docs/PLAN.md §6.
"""

from __future__ import annotations

import asyncio
import hashlib
import json
import random
from typing import TYPE_CHECKING

from ..config import BehaviorSettings
from .session import Fingerprint

if TYPE_CHECKING:
    from playwright.async_api import Page

# Curated, internally-consistent device profiles. Each keeps UA ↔ platform ↔ WebGL ↔ hardware in
# agreement; viewport is jittered per-identity. Keep the Chrome major version current.
_CHROME_MAJOR = 140

_DEVICE_PROFILES: list[dict] = [
    {
        "os": "windows",
        "platform": "Win32",
        "ua_platform": "Windows",
        "ua_tpl": (
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
            "(KHTML, like Gecko) Chrome/{major}.0.0.0 Safari/537.36"
        ),
        "screens": [(1920, 1080), (2560, 1440), (1536, 864)],
        "dsf": [1.0, 1.25, 1.5],
        "device_memory": [8, 16],
        "hardware_concurrency": [8, 12, 16],
        "webgl_vendor": "Google Inc. (NVIDIA)",
        "webgl_renderer": (
            "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)"
        ),
    },
    {
        "os": "windows",
        "platform": "Win32",
        "ua_platform": "Windows",
        "ua_tpl": (
            "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 "
            "(KHTML, like Gecko) Chrome/{major}.0.0.0 Safari/537.36"
        ),
        "screens": [(1920, 1080), (1366, 768)],
        "dsf": [1.0, 1.25],
        "device_memory": [8],
        "hardware_concurrency": [4, 8],
        "webgl_vendor": "Google Inc. (Intel)",
        "webgl_renderer": (
            "ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)"
        ),
    },
    {
        "os": "macos",
        "platform": "MacIntel",
        "ua_platform": "macOS",
        "ua_tpl": (
            "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 "
            "(KHTML, like Gecko) Chrome/{major}.0.0.0 Safari/537.36"
        ),
        "screens": [(2560, 1440), (1728, 1117), (1512, 982)],
        "dsf": [2.0],
        "device_memory": [8, 16],
        "hardware_concurrency": [8, 10, 12],
        "webgl_vendor": "Google Inc. (Apple)",
        "webgl_renderer": "ANGLE (Apple, ANGLE Metal Renderer: Apple M1 Pro, Unspecified Version)",
    },
]

# Reasonable geo defaults; override per-identity to match the proxy region.
_DEFAULT_GEO = {
    "locale": "en-US",
    "timezone_id": "America/New_York",
    "accept_language": "en-US,en;q=0.9",
}


def _rng_for(seed: str | None) -> random.Random:
    """Deterministic RNG per identity so a given account keeps a stable fingerprint."""
    if seed is None:
        return random.Random()
    digest = hashlib.sha256(seed.encode()).hexdigest()
    return random.Random(int(digest[:16], 16))


def generate_fingerprint(
    seed: str | None = None,
    *,
    locale: str | None = None,
    timezone_id: str | None = None,
    accept_language: str | None = None,
    chrome_major: int = _CHROME_MAJOR,
) -> Fingerprint:
    """Produce a consistent fingerprint. Same ``seed`` → same fingerprint (sticky identity)."""
    rng = _rng_for(seed)
    p = rng.choice(_DEVICE_PROFILES)
    screen = rng.choice(p["screens"])
    # Viewport is the window inner size: slightly smaller than the screen, with jitter.
    vw = screen[0] - rng.choice([0, 16, 32]) - rng.randint(0, 40)
    vh = screen[1] - rng.choice([79, 87, 111, 139])  # chrome/OS/tab chrome height
    return Fingerprint(
        user_agent=p["ua_tpl"].format(major=chrome_major),
        platform=p["platform"],
        ua_platform=p["ua_platform"],
        viewport=(max(1024, vw), max(720, vh)),
        screen=screen,
        device_scale_factor=rng.choice(p["dsf"]),
        locale=locale or _DEFAULT_GEO["locale"],
        timezone_id=timezone_id or _DEFAULT_GEO["timezone_id"],
        accept_language=accept_language or _DEFAULT_GEO["accept_language"],
        device_memory=rng.choice(p["device_memory"]),
        hardware_concurrency=rng.choice(p["hardware_concurrency"]),
        webgl_vendor=p["webgl_vendor"],
        webgl_renderer=p["webgl_renderer"],
        color_scheme="light",
    )


def build_stealth_init_script(fp: Fingerprint) -> str:
    """JS injected before page scripts so device props match the chosen fingerprint."""
    cfg = json.dumps(
        {
            "deviceMemory": fp.device_memory,
            "hardwareConcurrency": fp.hardware_concurrency,
            "platform": fp.platform,
            "languages": [x.split(";")[0] for x in fp.accept_language.split(",")],
            "webglVendor": fp.webgl_vendor,
            "webglRenderer": fp.webgl_renderer,
        }
    )
    return (
        "(() => { const c = " + cfg + ";\n"
        "  const def = (o, k, v) => { try { Object.defineProperty(o, k, { get: () => v, "
        "configurable: true }); } catch (e) {} };\n"
        "  def(navigator, 'deviceMemory', c.deviceMemory);\n"
        "  def(navigator, 'hardwareConcurrency', c.hardwareConcurrency);\n"
        "  def(navigator, 'platform', c.platform);\n"
        "  def(navigator, 'languages', c.languages);\n"
        "  const patchGL = (proto) => { if (!proto) return; const gp = proto.getParameter;\n"
        "    proto.getParameter = function(p) {\n"
        "      if (p === 37445) return c.webglVendor;   // UNMASKED_VENDOR_WEBGL\n"
        "      if (p === 37446) return c.webglRenderer; // UNMASKED_RENDERER_WEBGL\n"
        "      return gp.call(this, p); }; };\n"
        "  try { patchGL(WebGLRenderingContext.prototype); } catch (e) {}\n"
        "  try { patchGL(WebGL2RenderingContext.prototype); } catch (e) {}\n"
        "})();"
    )


# --------------------------------------------------------------------------------------
# Human-like input
# --------------------------------------------------------------------------------------


async def think(behavior: BehaviorSettings) -> None:
    """Pause as if reading/composing before acting."""
    await asyncio.sleep(random.uniform(behavior.think_min_ms, behavior.think_max_ms) / 1000.0)


async def human_type(
    page: "Page",
    selector_or_locator,
    text: str,
    behavior: BehaviorSettings,
    *,
    typo_rate: float = 0.02,
) -> None:
    """Type ``text`` with per-keystroke jitter and occasional typo→backspace corrections.

    ``selector_or_locator`` may be a CSS string or a Playwright Locator.
    Long generated prompts (notably inward-MCP tool catalogs) are inserted in
    one DOM-supported fill operation so typing time cannot consume the entire
    ask timeout before submission.
    """
    locator = page.locator(selector_or_locator) if isinstance(selector_or_locator, str) else selector_or_locator
    try:
        await locator.click(timeout=5000)
    except Exception:  # element may be visible but animated in the responsive mobile composer
        await locator.focus()
    if len(text) > 400:
        await locator.fill(text)
        return
    for ch in text:
        if ch != " " and random.random() < typo_rate:
            wrong = random.choice("abcdefghijklmnopqrstuvwxyz")
            await page.keyboard.type(wrong, delay=_keydelay(behavior))
            await asyncio.sleep(random.uniform(0.05, 0.2))
            await page.keyboard.press("Backspace")
        await page.keyboard.type(ch, delay=_keydelay(behavior))


def _keydelay(behavior: BehaviorSettings) -> float:
    return random.uniform(behavior.keystroke_min_ms, behavior.keystroke_max_ms)


async def human_mouse_move(page: "Page", x: float, y: float, *, steps: int = 0) -> None:
    """Best-effort jittered mouse move toward (x, y). Failures are ignored (headless has no mouse)."""
    try:
        steps = steps or random.randint(8, 20)
        await page.mouse.move(x, y, steps=steps)
    except Exception:  # noqa: BLE001
        pass


__all__ = [
    "build_stealth_init_script",
    "generate_fingerprint",
    "human_mouse_move",
    "human_type",
    "think",
]

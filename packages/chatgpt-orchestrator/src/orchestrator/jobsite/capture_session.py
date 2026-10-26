"""One-time interactive credential capture for a job-site account.

Run this by hand, from a machine/session with a real display (NOT headless, NOT xvfb -- a human
needs to actually see and click through the login, including any MFA/OTP/CAPTCHA step, which this
codebase will never attempt programmatically). On the devbox that means either an X11-forwarded /
VNC session, or simplest: run it on a machine that has a normal desktop (e.g. the Mac), then copy
the resulting storage_state.json over to the devbox at the path configured in ORCH_JOBSITE_ACCOUNTS.

Usage:
    uv run python -m orchestrator.jobsite.capture_session --site itviec --out storage_state/itviec.storage_state.json
    uv run python -m orchestrator.jobsite.capture_session --site linkedin --out storage_state/linkedin.storage_state.json

What it does:
  1. Launches a headed Chromium (or real Chrome channel) with a fresh, empty context.
  2. Navigates to the site's login page.
  3. Waits for you to log in by hand and press Enter in this terminal.
  4. Saves ``context.storage_state()`` to --out and exits.

It captures cookies + localStorage only -- never a password, OTP, or any other credential text.
"""

from __future__ import annotations

import argparse
import asyncio
from pathlib import Path

_LOGIN_URLS = {
    "itviec": "https://itviec.com/sign_in",
    "linkedin": "https://www.linkedin.com/login",
}

_HOME_HINTS = {
    # Substrings expected in the URL once actually logged in, used only to print a helpful
    # reminder -- capture always waits for an explicit Enter, it never auto-detects "done".
    "itviec": "itviec.com",
    "linkedin": "linkedin.com/feed",
}


async def _run(site: str, out: str) -> None:
    if site not in _LOGIN_URLS:
        raise SystemExit(f"unknown --site {site!r}; expected one of {sorted(_LOGIN_URLS)}")

    from patchright.async_api import async_playwright

    out_path = Path(out)
    out_path.parent.mkdir(parents=True, exist_ok=True)

    async with async_playwright() as pw:
        browser = await pw.chromium.launch(headless=False, channel="chrome")
        context = await browser.new_context()
        page = await context.new_page()
        await page.goto(_LOGIN_URLS[site])

        print(f"\nA browser window is open at {_LOGIN_URLS[site]}.")
        print(f"Log in by hand as the real {site} account -- solve any MFA/OTP/CAPTCHA yourself.")
        print(f"Once you're fully logged in (should land around {_HOME_HINTS[site]}), come back")
        print("here and press Enter. Nothing is saved until you do.\n")
        await asyncio.get_event_loop().run_in_executor(None, input, "Press Enter when logged in: ")

        await context.storage_state(path=str(out_path))
        await browser.close()

    print(f"Saved session to {out_path}. Point ORCH_JOBSITE_ACCOUNTS's storage_state at this path.")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--site", required=True, choices=sorted(_LOGIN_URLS))
    parser.add_argument("--out", required=True, help="Path to write storage_state.json to")
    args = parser.parse_args()
    asyncio.run(_run(args.site, args.out))


if __name__ == "__main__":
    main()

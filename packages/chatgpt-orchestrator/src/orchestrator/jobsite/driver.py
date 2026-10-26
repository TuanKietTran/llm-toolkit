"""Drives search + apply on one job-site session. Mirrors chatgpt/driver.py's shape (helpers ->
state detection -> action), adapted for arbitrary third-party sites instead of one known SPA.

Every apply() call goes through the same sequence regardless of site:
  1. open the posting URL fresh (never reuse a stale in-app panel)
  2. CAPTCHA check -> CaptchaEncountered (caller must skip, never retry)
  3. locate Apply/Easy-Apply, click it
  4. CAPTCHA check again (some sites gate the apply click itself)
  5. explicitly choose "upload new file" rather than accept a pre-selected stale CV (known quirk)
  6. upload the given cv_path, fill any cover-note field
  7. submit; if nothing visibly changed, click once more (known "needs a second click" quirk)
  8. CAPTCHA check again, then verify success via text/URL -- never assumed from the click alone
"""

from __future__ import annotations

import asyncio
import base64
from pathlib import Path
from typing import TYPE_CHECKING

from ..browser.session import BrowserSession
from ..browser.stealth import human_type, think
from ..config import Settings
from ..telemetry import get_logger, get_metrics, save_failure_snapshot
from . import selectors as sel
from .exceptions import CaptchaEncountered
from .schemas import ApplyResult, JobLead

if TYPE_CHECKING:
    from playwright.async_api import Locator, Page

log = get_logger(__name__)
metrics = get_metrics()


class JobSiteDriver:
    """Drives one job-site session (search + apply). One instance per site per call."""

    def __init__(self, site: str, session: BrowserSession, settings: Settings) -> None:
        self.site = site
        self.session = session
        self.settings = settings
        self._behavior = settings.behavior
        self._nav_timeout = settings.browser.nav_timeout_ms

    # ---- low-level helpers (same shape as ChatGPTDriver) ------------------------------

    async def _page(self) -> "Page":
        return await self.session.page()

    async def _first_visible(self, candidates: list[str]) -> "Locator | None":
        page = await self._page()
        for css in candidates:
            try:
                loc = page.locator(css).first
                if await loc.count() > 0 and await loc.is_visible():
                    return loc
            except Exception:  # noqa: BLE001
                continue
        return None

    async def _first_by_text(self, texts: list[str]) -> "Locator | None":
        """Find the first visible button/link whose accessible text matches one of ``texts``."""
        page = await self._page()
        for t in texts:
            for role in ("button", "link"):
                try:
                    loc = page.get_by_role(role, name=t, exact=False).first
                    if await loc.count() > 0 and await loc.is_visible():
                        return loc
                except Exception:  # noqa: BLE001
                    continue
            try:
                loc = page.get_by_text(t, exact=False).first
                if await loc.count() > 0 and await loc.is_visible():
                    return loc
            except Exception:  # noqa: BLE001
                continue
        return None

    async def _page_text(self) -> str:
        page = await self._page()
        try:
            return (await page.locator("body").inner_text()).lower()
        except Exception:  # noqa: BLE001
            try:
                return (await page.content()).lower()
            except Exception:  # noqa: BLE001
                return ""

    async def _snapshot(self, reason: str) -> Path | None:
        page = await self._page()
        html = shot = None
        try:
            html = await page.content()
        except Exception:  # noqa: BLE001
            pass
        try:
            shot = await page.screenshot()
        except Exception:  # noqa: BLE001
            pass
        return save_failure_snapshot(
            f"{self.site}_{reason}",
            root=str(self.settings.snapshot_dir),
            html=html,
            screenshot=shot,
            url=page.url,
            meta={"site": self.site},
        )

    async def read_context(self, *, include_screenshot: bool = False) -> dict:
        """Same contract as ChatGPTDriver.read_context -- for a human/agent to sanity-check what
        a headless jobsite session is actually looking at."""
        page = await self._page()
        try:
            text = (await page.locator("body").inner_text()).strip()
        except Exception:  # noqa: BLE001
            text = ""
        result: dict = {"url": page.url, "text": text[:8000]}
        if include_screenshot:
            try:
                shot = await page.screenshot()
                result["screenshot_b64"] = base64.b64encode(shot).decode("ascii")
            except Exception:  # noqa: BLE001
                result["screenshot_b64"] = None
        return result

    # ---- CAPTCHA / bot-check detection (hard rule: never solve, always skip) ----------

    async def _check_captcha(self) -> None:
        page = await self._page()
        text = await self._page_text()
        for marker in sel.CAPTCHA_TEXT_MARKERS:
            if marker in text:
                shot_dir = await self._snapshot("captcha_text")
                metrics.incr("jobsite_captcha", site=self.site)
                raise CaptchaEncountered(
                    f"CAPTCHA/bot-check text {marker!r} present on {page.url}",
                    url=page.url,
                    evidence=str(shot_dir) if shot_dir else None,
                )
        try:
            for frame in page.frames:
                for marker in sel.CAPTCHA_FRAME_URL_MARKERS:
                    if marker in (frame.url or ""):
                        shot_dir = await self._snapshot("captcha_frame")
                        metrics.incr("jobsite_captcha", site=self.site)
                        raise CaptchaEncountered(
                            f"CAPTCHA iframe ({marker}) present on {page.url}",
                            url=page.url,
                            evidence=str(shot_dir) if shot_dir else None,
                        )
        except CaptchaEncountered:
            raise
        except Exception:  # noqa: BLE001 - frame enumeration racing navigation is not fatal
            pass

    # ---- search ------------------------------------------------------------------------

    async def search(self, url: str) -> list[JobLead]:
        """Navigate to a prebuilt search URL and parse job cards. Site-specific card parsing."""
        page = await self._page()
        try:
            await page.goto(url, wait_until="domcontentloaded", timeout=self._nav_timeout)
        except Exception:  # noqa: BLE001
            await self._snapshot("search_navigation_failed")
            raise
        await self._check_captcha()
        await think(self._behavior)

        if self.site == "itviec":
            return await self._parse_itviec_results()
        if self.site == "linkedin":
            return await self._parse_linkedin_results()
        raise ValueError(f"unknown site {self.site!r}")

    async def _parse_itviec_results(self) -> list[JobLead]:
        page = await self._page()
        cards = page.locator(sel.ITviec.job_card)
        n = await cards.count()
        leads: list[JobLead] = []
        for i in range(n):
            card = cards.nth(i)
            try:
                title_link = card.locator(sel.ITviec.job_title_link).first
                title = (await title_link.inner_text()).strip()
                href = await title_link.get_attribute("href") or ""
                company_link = card.locator(sel.ITviec.job_company_link).first
                company = (await company_link.get_attribute("title") or "").strip()
                if not company:
                    company = (await company_link.inner_text()).strip()
            except Exception:  # noqa: BLE001 - one broken card must not abort the whole page
                continue
            if not title or not href:
                continue
            leads.append(
                JobLead(source="itviec", title=title, company=company, url=href.split("?")[0])
            )
        log.info("jobsite_search_parsed", site="itviec", url=page.url, count=len(leads))
        return leads

    async def _parse_linkedin_results(self) -> list[JobLead]:
        page = await self._page()
        cards = page.locator(sel.LinkedIn.job_card)
        n = await cards.count()
        leads: list[JobLead] = []
        for i in range(n):
            card = cards.nth(i)
            try:
                title = (await card.locator(sel.LinkedIn.job_title).first.inner_text()).strip()
                company = (await card.locator(sel.LinkedIn.job_company).first.inner_text()).strip()
                href = await card.locator(sel.LinkedIn.job_link).first.get_attribute("href") or ""
            except Exception:  # noqa: BLE001
                continue
            if not title or not href:
                continue
            leads.append(
                JobLead(source="linkedin", title=title, company=company, url=href.split("?")[0])
            )
        log.info("jobsite_search_parsed", site="linkedin", url=page.url, count=len(leads))
        return leads

    # ---- apply ---------------------------------------------------------------------------

    async def apply(self, lead: JobLead, cv_path: str, cover_note: str = "") -> ApplyResult:
        """Open ``lead.url`` fresh and drive the site's apply flow. Never raises on expected
        outcomes (captcha/login/verification-failed) -- those come back as a non-ok ApplyResult
        so a batch of leads can keep going; only unexpected errors propagate."""
        page = await self._page()
        try:
            await page.goto(lead.url, wait_until="domcontentloaded", timeout=self._nav_timeout)
        except Exception as exc:  # noqa: BLE001
            await self._snapshot("apply_navigation_failed")
            return ApplyResult(
                ok=False, verified=False, lead_url=lead.url, status="failed",
                detail=f"navigation failed: {exc}",
            )

        try:
            await self._check_captcha()
        except CaptchaEncountered as exc:
            log.warning("jobsite_captcha_skip", site=self.site, url=lead.url)
            return ApplyResult(
                ok=False, verified=False, lead_url=lead.url, status="skipped_captcha",
                detail=str(exc), screenshot_path=exc.evidence,
            )
        await think(self._behavior)

        apply_texts = sel.ITviec.apply_button_text if self.site == "itviec" else (
            sel.LinkedIn.easy_apply_button_text
        )
        apply_btn = await self._first_by_text(apply_texts)
        if apply_btn is None:
            if self.site == "linkedin":
                # Distinguish "no button found at all" from "this is an external-ATS apply" so the
                # caller can tell the difference between a real failure and an out-of-scope lead.
                external_btn = await self._first_by_text(sel.LinkedIn.external_apply_button_text)
                if external_btn is not None:
                    return ApplyResult(
                        ok=False, verified=False, lead_url=lead.url, status="failed",
                        detail="only an external-ATS Apply button was found; this driver only "
                        "handles LinkedIn Easy Apply, not third-party ATS pages",
                    )
            await self._snapshot("apply_button_missing")
            return ApplyResult(
                ok=False, verified=False, lead_url=lead.url, status="failed",
                detail="no Apply button found",
            )

        try:
            await apply_btn.click()
        except Exception as exc:  # noqa: BLE001
            await self._snapshot("apply_click_failed")
            return ApplyResult(
                ok=False, verified=False, lead_url=lead.url, status="failed",
                detail=f"apply click failed: {exc}",
            )
        await think(self._behavior)

        try:
            await self._check_captcha()
        except CaptchaEncountered as exc:
            log.warning("jobsite_captcha_skip", site=self.site, url=lead.url, stage="post_apply_click")
            return ApplyResult(
                ok=False, verified=False, lead_url=lead.url, status="skipped_captcha",
                detail=str(exc), screenshot_path=exc.evidence,
            )

        # Explicit "upload new" step -- never accept a pre-selected stale CV silently.
        upload_texts = (
            sel.ITviec.upload_new_cv_text if self.site == "itviec" else sel.LinkedIn.upload_new_resume_text
        )
        upload_trigger = await self._first_by_text(upload_texts)
        if upload_trigger is not None:
            try:
                await upload_trigger.click()
                await think(self._behavior)
            except Exception:  # noqa: BLE001 - fall through to the direct file input either way
                pass

        file_input = await self._first_visible([sel.ITviec.file_input if self.site == "itviec" else sel.LinkedIn.file_input])
        if file_input is None:
            # Some flows keep the <input type=file> hidden (styled button triggers it) --
            # Playwright can still set files on a hidden input, so fall back to any() in DOM.
            page2 = await self._page()
            loc = page2.locator("input[type='file']").first
            if await loc.count() > 0:
                file_input = loc
        if file_input is not None:
            try:
                await file_input.set_input_files(cv_path)
                await think(self._behavior)
            except Exception as exc:  # noqa: BLE001
                await self._snapshot("cv_upload_failed")
                return ApplyResult(
                    ok=False, verified=False, lead_url=lead.url, status="failed",
                    detail=f"CV upload failed: {exc}", cv_path=cv_path,
                )
            # Verify the right filename actually shows selected before proceeding (known quirk:
            # forms sometimes silently keep a previously-uploaded file instead).
            filename = Path(cv_path).name
            page_text_after = await self._page_text()
            if filename.lower() not in page_text_after:
                await self._snapshot("cv_filename_not_confirmed")
                log.warning(
                    "jobsite_cv_filename_unconfirmed", site=self.site, url=lead.url, filename=filename
                )
                # Not fatal by itself (some UIs show a generic "1 file" label) -- proceed but the
                # caller should treat this ApplyResult's evidence_text carefully.

        if cover_note:
            note_field = await self._first_visible(
                [sel.ITviec.cover_note_textarea] if self.site == "itviec" else []
            )
            if note_field is not None:
                try:
                    await human_type(await self._page(), note_field, cover_note, self._behavior)
                except Exception:  # noqa: BLE001 - a missing cover-note field is not fatal
                    pass

        submit_texts = sel.ITviec.submit_button_text if self.site == "itviec" else sel.LinkedIn.submit_button_text
        result = await self._submit_and_verify(lead, submit_texts, cv_path)
        return result

    async def _submit_and_verify(
        self, lead: JobLead, submit_texts: list[str], cv_path: str
    ) -> ApplyResult:
        success_markers = (
            sel.ITviec.success_text_markers if self.site == "itviec" else sel.LinkedIn.success_text_markers
        )
        url_before = (await self._page()).url

        for attempt in range(2):  # known quirk: some Apply flows need a second click to register
            submit_btn = await self._first_by_text(submit_texts)
            if submit_btn is None:
                break
            try:
                await submit_btn.click()
            except Exception as exc:  # noqa: BLE001
                await self._snapshot("submit_click_failed")
                return ApplyResult(
                    ok=False, verified=False, lead_url=lead.url, status="failed",
                    detail=f"submit click failed: {exc}", cv_path=cv_path,
                )
            await asyncio.sleep(1.5)

            try:
                await self._check_captcha()
            except CaptchaEncountered as exc:
                log.warning("jobsite_captcha_skip", site=self.site, url=lead.url, stage="post_submit")
                return ApplyResult(
                    ok=False, verified=False, lead_url=lead.url, status="skipped_captcha",
                    detail=str(exc), screenshot_path=exc.evidence, cv_path=cv_path,
                )

            text = await self._page_text()
            url_after = (await self._page()).url
            hit = next((m for m in success_markers if m.lower() in text), None)
            if hit or url_after != url_before:
                shot_path = await self._snapshot("apply_submitted")  # audit trail, not a failure
                metrics.incr("jobsite_applications_submitted", site=self.site)
                log.info("jobsite_applied", site=self.site, url=lead.url, company=lead.company)
                return ApplyResult(
                    ok=True,
                    verified=True,
                    lead_url=lead.url,
                    status="submitted",
                    detail=hit or f"URL changed to {url_after}",
                    evidence_text=hit or "",
                    screenshot_path=str(shot_path) if shot_path else None,
                    cv_path=cv_path,
                )
            # No success signal yet -- if a submit button is still present, loop for the known
            # "needs a second click" quirk; otherwise stop.

        shot_path = await self._snapshot("apply_unverified")
        return ApplyResult(
            ok=False,
            verified=False,
            lead_url=lead.url,
            status="failed",
            detail="submit clicked but no success text/URL change observed -- not counting as applied",
            screenshot_path=str(shot_path) if shot_path else None,
            cv_path=cv_path,
        )


__all__ = ["JobSiteDriver"]

"""Per-site selectors and search-URL builders.

The search-result selectors below were captured by fetching each site's live public search HTML
(no login) on 2026-08-31 and reading the actual server-rendered markup -- not guessed. Apply-flow
selectors (behind login) could NOT be verified the same way (no credentialed session exists yet
on this box -- see manager.py's docstring), so they're written defensively: prefer Playwright's
role/text locators (which tolerate class-name churn) over deep CSS chains, and every apply() call
re-verifies via read-back text/URL rather than trusting a selector matched at all.

If a site redesigns its search results DOM, `search()` breaking loudly (zero results parsed) is
the intended failure mode -- it must never silently return an empty list and let the caller assume
"no matching jobs today".
"""

from __future__ import annotations

from urllib.parse import quote_plus

# --------------------------------------------------------------------------------------
# ITviec
# --------------------------------------------------------------------------------------

ITVIEC_BASE = "https://itviec.com"


def itviec_search_url(query: str, *, city: str = "ho-chi-minh-city", remote: bool = False) -> str:
    """Build an ITviec search URL. ``city`` is itviec's own slug (e.g. "ho-chi-minh-city",
    "ha-noi", or "remote" for their dedicated remote-jobs listing)."""
    slug = "remote-jobs" if remote else f"it-jobs/{city}"
    return f"{ITVIEC_BASE}/{slug}?query={quote_plus(query)}"


class ITviec:
    # Confirmed against live markup 2026-08-31: `div.job-card[data-job-key]` is the card
    # container; job-selection-job-slug-value / job-selection-job-url-value hold the identifying
    # slug + preview-pane URL, but the plain `<h3><a href=...>` inside is the real posting URL
    # and is what gets opened in a new context for apply() -- prefer that over the data-* preview
    # URL, which points at an in-page JD panel, not a standalone page.
    job_card = "div.job-card"
    job_title_link = "div.job-card h3 a"  # text = title, href = posting URL
    job_company_link = "div.job-card a[href^='/companies/']"  # `title` attr = company name

    # Apply flow (unverified -- no login capture yet). ITviec's apply is a modal from the job
    # detail page with a "Ứng tuyển" / "Apply" button, a CV picker that defaults to the most
    # recently uploaded CV (the known "stale pre-selected CV" quirk -- always look for and click
    # an explicit "upload new" / "Tải CV khác lên" control instead of accepting the default), a
    # native `<input type=file>` for the new upload, an optional cover-letter textarea, and a
    # final submit button.
    apply_button_text = ["Ứng tuyển", "Apply now", "Apply"]
    upload_new_cv_text = ["Tải CV khác lên", "Upload new CV", "Upload a different CV", "Tải lên CV mới"]
    file_input = "input[type='file']"
    cover_note_textarea = "textarea"
    submit_button_text = ["Gửi hồ sơ", "Submit application", "Nộp đơn", "Send"]
    success_text_markers = [
        "đã được gửi đi",  # "...has been sent"
        "Ứng tuyển thành công",
        "application has been sent",
        "successfully applied",
        "Cảm ơn bạn đã ứng tuyển",
    ]


# --------------------------------------------------------------------------------------
# LinkedIn
# --------------------------------------------------------------------------------------

LINKEDIN_BASE = "https://www.linkedin.com"


def linkedin_search_url(
    query: str, *, location: str = "Ho Chi Minh City, Vietnam", remote: bool = False
) -> str:
    """Build a LinkedIn jobs search URL. f_WT=2 filters to remote-only when requested."""
    url = (
        f"{LINKEDIN_BASE}/jobs/search/?keywords={quote_plus(query)}"
        f"&location={quote_plus(location)}"
    )
    if remote:
        url += "&f_WT=2"
    return url


class LinkedIn:
    # Confirmed against live guest-search markup 2026-08-31.
    job_card = "div.base-card[data-entity-urn]"
    job_title = "h3.base-search-card__title"
    job_company = "h4.base-search-card__subtitle a"
    job_link = "a.base-card__full-link"

    # Apply flow (unverified -- no login capture yet). Two shapes: "Easy Apply" (in-page modal,
    # entirely on linkedin.com) or a plain "Apply" that opens the employer's own ATS in a new tab
    # (Dayforce / Mantu-Amaris per the prior manual campaign) -- the latter needs a *different*,
    # ATS-specific driver this module does not attempt; jobsite_apply() should detect the tab/URL
    # change to a non-linkedin.com domain and report it as "external_ats" rather than guessing at
    # a generic form-fill.
    easy_apply_button_text = ["Easy Apply"]
    external_apply_button_text = ["Apply"]  # opens employer ATS in a new tab, not handled here
    easy_apply_modal = "div.jobs-easy-apply-modal, div[data-test-modal-id='easy-apply-modal']"
    upload_new_resume_text = ["Upload resume", "Upload new resume", "Replace resume"]
    file_input = "input[type='file']"
    next_button_text = ["Next", "Review"]
    submit_button_text = ["Submit application"]
    success_text_markers = [
        "Your application was sent",
        "Application submitted",
        "was sent to",
    ]


# --------------------------------------------------------------------------------------
# Cross-site CAPTCHA / bot-check detection
# --------------------------------------------------------------------------------------

# Deliberately broad and checked at every stage (after nav, after click, after submit). A false
# positive here just means an extra skipped lead; a false negative means attempting to solve a
# CAPTCHA, which is the one thing this module must never do.
CAPTCHA_TEXT_MARKERS = [
    "verify you are human",
    "verify you're human",
    "are you a robot",
    "i'm not a robot",
    "unusual traffic",
    "unusual activity",
    "checking your browser",
    "please complete the security check",
    "additional verification required",
    "security check",
    "captcha",
]

CAPTCHA_FRAME_URL_MARKERS = [
    "recaptcha",
    "hcaptcha",
    "captcha-delivery.com",  # DataDome
    "challenges.cloudflare.com",
    "perimeterx",
    "arkoselabs",  # LinkedIn/Microsoft's provider (FunCaptcha)
]

__all__ = [
    "CAPTCHA_FRAME_URL_MARKERS",
    "CAPTCHA_TEXT_MARKERS",
    "ITviec",
    "LinkedIn",
    "itviec_search_url",
    "linkedin_search_url",
]

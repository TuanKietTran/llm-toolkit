"""Typed shapes for jobsite MCP tool results (see ``server/schemas.py`` for the ChatGPT-side
equivalents; FastMCP infers tool *inputs* from function signatures, these are for outputs)."""

from __future__ import annotations

from pydantic import BaseModel


class JobLead(BaseModel):
    """One live search result. Shape intentionally matches leads.json's historical schema
    (source/title/company/url/snippet/date) plus fields only a live search can populate."""

    source: str  # "itviec" | "linkedin"
    title: str
    company: str
    url: str
    snippet: str = ""
    date: str = ""
    location: str = ""
    remote: bool | None = None


class ApplyResult(BaseModel):
    """Outcome of one apply attempt. ``ok`` is only True after positive post-submit verification
    (success text or URL change actually observed) -- a click alone never sets it."""

    ok: bool
    verified: bool
    lead_url: str
    status: str  # "submitted" | "skipped_captcha" | "skipped_login_required" | "failed"
    detail: str = ""
    evidence_text: str = ""
    screenshot_path: str | None = None
    cv_path: str | None = None


__all__ = ["ApplyResult", "JobLead"]

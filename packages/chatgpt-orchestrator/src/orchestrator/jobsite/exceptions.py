"""Exceptions for jobsite search/apply. Mirrors ``chatgpt/exceptions.py``'s shape."""

from __future__ import annotations


class JobSiteError(RuntimeError):
    """Base class for all job-site automation failures."""


class LoginRequired(JobSiteError):
    """The credentialed session is not actually logged in (storage_state missing/expired).

    Never attempt to log in interactively from here -- this always means a human needs to
    re-run the one-time capture flow (see ``manager.py``) and refresh the storage_state file.
    """


class CaptchaEncountered(JobSiteError):
    """A CAPTCHA / bot-check was detected. Always abort the current lead, never attempt it.

    This is a hard, non-negotiable rule (see job-cv-finder/prompts/orchestrate.md). Callers must
    catch this, log the lead as skipped with the reason, and move on -- never retry, never click
    through, never wait it out.
    """

    def __init__(self, message: str, *, url: str | None = None, evidence: str | None = None):
        super().__init__(message)
        self.url = url
        self.evidence = evidence


class ApplyVerificationFailed(JobSiteError):
    """A submit button was clicked but post-submit verification did not find a success signal.

    Raised instead of silently assuming success -- per the known "second click needed" and
    "stale pre-selected CV" site quirks, a click alone is never sufficient evidence of a real
    submission.
    """


__all__ = ["ApplyVerificationFailed", "CaptchaEncountered", "JobSiteError", "LoginRequired"]

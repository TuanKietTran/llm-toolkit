"""Job-site search + apply automation (ITviec, LinkedIn).

Unlike ``chatgpt/`` (a pool of many *anonymous* ChatGPT identities scaled by an autoscaler), this
package drives a small, fixed set of *credentialed* real-account sessions -- one per site -- reused
sequentially across a run. It shares the same underlying :class:`~orchestrator.browser.manager.
BrowserManager` process (one Chromium, many isolated contexts) but intentionally skips the
budget/cooldown/proxy-rotation machinery in ``accounts/`` -- that machinery exists to keep many
disposable anonymous identities alive; a real ITviec/LinkedIn login is a single durable identity
that should look like one consistent human, not be rotated.

See ``manager.py`` for the credential bootstrap story (storage_state, captured out-of-band).
"""

from .driver import JobSiteDriver
from .exceptions import ApplyVerificationFailed, CaptchaEncountered, JobSiteError, LoginRequired
from .manager import JobSiteManager
from .schemas import ApplyResult, JobLead

__all__ = [
    "ApplyResult",
    "ApplyVerificationFailed",
    "CaptchaEncountered",
    "JobLead",
    "JobSiteDriver",
    "JobSiteError",
    "JobSiteManager",
    "LoginRequired",
]

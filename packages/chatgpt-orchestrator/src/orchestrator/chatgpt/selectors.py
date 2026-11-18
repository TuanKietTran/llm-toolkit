"""ALL brittle ChatGPT DOM selectors, centralized.

The ChatGPT web UI changes often; when it breaks, patch here (and only here). Each field is an
ordered list of candidate selectors — the driver uses the first that matches, so we can carry a
current selector plus older fallbacks. Verify against the live site during the M1 feasibility gate.
"""

from __future__ import annotations

# Text fragments (lowercased) used for state detection via page content.
CHALLENGE_TEXT = [
    "verify you are human",
    "verifying you are human",
    "just a moment",
    "checking your browser",
    "needs to review the security of your connection",
]
RATE_LIMIT_TEXT = [
    "you've reached our limit of messages",
    "you've reached the current usage cap",
    "you've hit the free plan limit",
    "please try again later",
    "you have reached your limit",
]
LOGIN_TEXT = [
    "log in to continue",
    "sign in to continue",
]

# Prompt input (ProseMirror contenteditable in current builds; textarea in older ones).
PROMPT_INPUT = [
    "textarea#mobile-composer-prompt",  # 2026-11 build: single <textarea name=prompt> composer
    "#prompt-textarea",
    "div[contenteditable='true']#prompt-textarea",
    "[data-testid='prompt-textarea']",
    "textarea[data-id]",
    "textarea[placeholder]",
]

# The *hydrated*, interactive composer only. Current builds SSR a hidden fallback
# ``<textarea class="…fallbackTextarea" name="prompt-textarea">`` (no id) before JS hydrates the real
# ProseMirror composer (a ``div#prompt-textarea``). Interacting with the fallback fails click/type
# actionability, so readiness waits on these id/testid-based selectors, which the fallback never
# matches. Deliberately excludes the bare ``textarea[placeholder]`` fallback in PROMPT_INPUT.
COMPOSER_READY = [
    "textarea#mobile-composer-prompt",  # 2026-11 build (desktop too); ProseMirror div is gone
    "#prompt-textarea",
    "div[contenteditable='true']#prompt-textarea",
    "[data-testid='prompt-textarea']",
    "textarea#mobile-composer-prompt",
]

# Send / stop buttons — the send↔stop transition is the primary completion signal.
SEND_BUTTON = [
    "[data-testid='send-button']",
    "button[data-testid='send-button']",
    "button[aria-label*='Send']",
]
STOP_BUTTON = [
    "[data-testid='stop-button']",
    "button[data-testid='stop-button']",
    "button[aria-label*='Stop']",
]

# Assistant messages and their rendered content.
ASSISTANT_MESSAGE = [
    "li[data-message-role='assistant']",  # 2026-11 build: <ol data-conversation-transcript> of <li>
    "[data-message-author-role='assistant']",
    "div[data-message-author-role='assistant']",
]
ASSISTANT_CONTENT = [
    "[data-assistant-markdown-root]",  # 2026-11 build
    ".markdown",
    "[data-message-content]",
    ".prose",
]
# Class present on the message element while it is still streaming (older builds).
STREAMING_MARKER = [
    # 2026-11 build: the assistant <li> gains data-message-complete once the reply settles, and the
    # markdown wrapper carries aria-busy=true while streaming.
    "li[data-message-role='assistant']:not([data-message-complete])",
    "[data-assistant-markdown-root] [aria-busy='true']",
    ".result-streaming",
    "[data-message-streaming='true']",
]

# Challenge / Turnstile widgets.
CHALLENGE_FRAME = [
    "iframe[src*='challenges.cloudflare.com']",
    "iframe[title*='Just a moment']",
    "#challenge-running",
    "#turnstile-wrapper",
]

# Login wall.
LOGIN_BUTTON = [
    "[data-testid='login-button']",
    "a[href*='auth.openai.com']",
    "button:has-text('Log in')",
]

# Dismissible modals/overlays before the composer is usable.
STAY_LOGGED_OUT = [
    "a:has-text('Stay logged out')",
    "text=Stay logged out",
]
DISMISS_BUTTONS = [
    "button:has-text('Okay, let’s go')",
    "button:has-text(\"Okay, let's go\")",
    "button:has-text('Got it')",
    # Cookie banner: deliberately NOT clicked. On the 2026-11 build, answering it either way
    # ("Accept all" or "Reject non-essential") leaves anonymous sends stuck on "Stop generating"
    # with no assistant message ever rendered. The banner doesn't block the composer.
    "[data-testid='close-button']",
    "button[aria-label='Close']",
]

__all__ = [
    "ASSISTANT_CONTENT",
    "ASSISTANT_MESSAGE",
    "CHALLENGE_FRAME",
    "CHALLENGE_TEXT",
    "COMPOSER_READY",
    "DISMISS_BUTTONS",
    "LOGIN_BUTTON",
    "LOGIN_TEXT",
    "PROMPT_INPUT",
    "RATE_LIMIT_TEXT",
    "SEND_BUTTON",
    "STAY_LOGGED_OUT",
    "STOP_BUTTON",
    "STREAMING_MARKER",
]

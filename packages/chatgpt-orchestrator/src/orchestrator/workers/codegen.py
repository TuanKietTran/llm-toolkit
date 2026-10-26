"""Turn a code request into a worker prompt, and a worker's reply back into clean code.

A ChatGPT browser worker answers in markdown — usually prose plus one or more fenced code blocks.
These pure helpers (no Odoo, no Playwright, no network) wrap a request so the reply is easy to parse,
and pull the code back out reliably: prefer a fenced block whose language tag matches what was
asked, else the longest block, else fall back to the whole reply.
"""

from __future__ import annotations

import re
from dataclasses import dataclass

# ```lang\n ...code... \n``` — language tag is optional; allow +/-/. for c++, f#, objective-c, etc.
_FENCE_RE = re.compile(r"```([\w+.#-]*)[ \t]*\n(.*?)```", re.DOTALL)


@dataclass
class CodeResult:
    """Extracted code plus what we could tell about it."""

    code: str
    language: str | None
    raw: str
    had_fence: bool


def build_code_prompt(spec: str, language: str | None = None) -> str:
    """Wrap a free-form code request so the worker returns one clean fenced block and no prose."""
    in_lang = f" in {language}" if language else ""
    tag = language or ""
    return (
        f"Write code{in_lang} for the request below. Respond with ONLY a single fenced code block "
        f"(```{tag} ... ```) containing the complete code — no explanation, comments-as-prose, or "
        f"text before or after the block.\n\nRequest:\n{spec}"
    )


def extract_code(reply: str, language: str | None = None) -> CodeResult:
    """Pull the code out of a worker reply.

    Preference order: a fenced block tagged with the requested ``language``, else the longest fenced
    block, else (no fence at all) the whole reply stripped. ``had_fence`` says which path was taken,
    so callers can flag "the worker wrapped prose we couldn't separate".
    """
    text = reply or ""
    matches = _FENCE_RE.findall(text)
    if matches:
        chosen: tuple[str, str] | None = None
        if language:
            for tag, body in matches:
                if tag.lower() == language.lower():
                    chosen = (tag, body)
                    break
        if chosen is None:
            chosen = max(matches, key=lambda m: len(m[1]))
        tag, body = chosen
        return CodeResult(
            code=body.strip("\n"),
            language=(tag or language or None),
            raw=text,
            had_fence=True,
        )
    return CodeResult(code=text.strip(), language=language, raw=text, had_fence=False)


__all__ = ["CodeResult", "build_code_prompt", "extract_code"]

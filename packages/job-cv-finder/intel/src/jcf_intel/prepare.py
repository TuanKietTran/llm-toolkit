"""Application preparation: CV selection, tailoring, cover letter, answers, validation.

Rules enforced here (and re-checked by Go):
- The tailored CV only REORDERS the selected Ruxt CV's own lines; it never adds text.
- Every candidate claim in a generated document is a statement with evidence fact ids.
- Model output (optional) must pass the same validation or it is discarded for the template.
- Questions are answered only from candidate-written preferences or evidenced facts;
  anything else stays "unknown" and becomes a candidate exception in Go.
"""

from __future__ import annotations

import json
import logging
import re
from typing import Any

import httpx

from . import ENGINE_NAME, ENGINE_VERSION
from .facts import Fact, extract_facts, skills_with_evidence
from .lexicon import find_skills
from .llm import ModelProvider, NullProvider, data_block

PROMPT_VERSION = "cover-letter.v2"
log = logging.getLogger("jcf_intel")


# ------------------------------------------------------------------ CV selection

def score_cv(cv: dict[str, Any], job: dict[str, Any]) -> float:
    text = cv["markdown"]
    cv_skills = find_skills(text)
    req = set(job.get("required_skills") or []) or find_skills(job.get("title", ""))
    pref = set(job.get("preferred_skills") or [])
    req_cov = len(req & cv_skills) / len(req) if req else 0.5
    pref_cov = len(pref & cv_skills) / len(pref) if pref else 0.5
    title_words = {w for w in re.findall(r"[a-z]{4,}", job.get("title", "").lower())}
    head = (cv.get("title", "") + " " + text[:400]).lower()
    title_cov = sum(1 for w in title_words if w in head) / len(title_words) if title_words else 0.0
    return round(0.65 * req_cov + 0.15 * pref_cov + 0.20 * title_cov, 4)


def select_cv(cvs: list[dict[str, Any]], job: dict[str, Any]) -> tuple[dict[str, Any], dict[str, Any]]:
    scored = sorted(((score_cv(cv, job), cv) for cv in cvs), key=lambda x: (-x[0], x[1]["cv_id"]))
    best_score, best = scored[0]
    req = set(job.get("required_skills") or [])
    covered = sorted(req & find_skills(best["markdown"]))
    reason = f"highest coverage of the job's stated skills ({len(covered)}/{len(req)}: {', '.join(covered) or 'none'})" if req else "best title/skill overlap; job lists few skills"
    sel = {"cv_id": best["cv_id"], "revision": best["revision"], "reason": reason, "score": min(1.0, best_score),
           "alternatives": [{"cv_id": cv["cv_id"], "revision": cv["revision"], "score": s} for s, cv in scored[1:]]}
    return best, sel


# ------------------------------------------------------------------ tailoring

_BULLET = re.compile(r"^(\s*)([-*+]|\d+\.)\s+(.*)$")


def tailor(cv: dict[str, Any], job: dict[str, Any], facts: list[Fact]) -> dict[str, Any]:
    """Reorder bullets inside each contiguous bullet block so job-relevant ones come first."""
    wanted = set(job.get("required_skills") or []) | set(job.get("preferred_skills") or []) | find_skills(job.get("title", ""))
    cv_doc, cv_ver = "cv:" + cv["cv_id"], str(cv["revision"])
    by_line = {f.evidence_location: f.fact_id for f in facts
               if f.source_document_id == cv_doc and f.source_version_id == cv_ver and f.category == "achievement"}
    lines = cv["markdown"].splitlines()
    out: list[str] = []
    statements: list[dict[str, Any]] = []
    i = 0
    while i < len(lines):
        if not _BULLET.match(lines[i]):
            out.append(lines[i])
            i += 1
            continue
        block: list[tuple[int, str]] = []
        while i < len(lines) and _BULLET.match(lines[i]):
            block.append((i + 1, lines[i]))
            i += 1
        rel = lambda item: -len(find_skills(item[1]) & wanted)  # noqa: E731
        for n, line in sorted(block, key=lambda it: (rel(it), it[0])):  # stable: original order breaks ties
            out.append(line)
            fid = by_line.get(f"L{n}")
            content = _BULLET.match(line).group(3).strip()  # type: ignore[union-attr]
            if fid and len(content) >= 3:
                statements.append({"text": content, "evidence_refs": [fid]})
    content = "\n".join(out)
    if cv["markdown"].endswith("\n"):
        content += "\n"
    return {"kind": "tailored_cv", "format": "markdown", "content": content, "statements": statements,
            "source_cv_id": cv["cv_id"], "source_cv_revision": cv["revision"]}


# ------------------------------------------------------------------ cover letter

def _fact_of(facts: list[Fact], category: str) -> Fact | None:
    return next((f for f in facts if f.category == category), None)


def template_cover_letter(job: dict[str, Any], cv: dict[str, Any], facts: list[Fact], match: dict[str, Any] | None) -> dict[str, Any]:
    evidence = skills_with_evidence(facts)
    req = list(job.get("required_skills") or []) or sorted(find_skills(job.get("title", "")))
    have = [s for s in req if s in evidence]
    lack = [s for s in req if s not in evidence]
    name = _fact_of(facts, "identity")
    statements: list[dict[str, Any]] = []
    parts = ["Dear Hiring Team,", "", f"I am applying for the {job['title']} role at {job['company']}."]
    if have:
        s = "My experience includes " + ", ".join(have) + "."
        parts.append(s)
        statements.append({"text": s, "evidence_refs": sorted({evidence[k][0] for k in have})})
    emp = _fact_of(facts, "employment")
    if emp:
        s = f"Most recently I worked as {emp.value}."
        parts.append(s)
        statements.append({"text": s, "evidence_refs": [emp.fact_id]})
    best_line = None
    for f in facts:
        if f.category == "achievement" and f.source_document_id == "cv:" + cv["cv_id"] and find_skills(f.value) & set(have):
            best_line = f
            break
    if best_line:
        s = f"For example: {best_line.value.rstrip('.')}."
        parts.append(s)
        statements.append({"text": s, "evidence_refs": [best_line.fact_id]})
    if lack:
        parts.append("The role also lists " + ", ".join(lack) + ", which my CV does not show; I am happy to discuss how I would close that gap.")
    parts += ["", "Kind regards,", name.value if name else ""]
    return {"kind": "cover_letter", "format": "text", "content": "\n".join(parts).strip() + "\n", "statements": statements,
            "source_cv_id": cv["cv_id"], "source_cv_revision": cv["revision"]}


def model_cover_letter(provider: ModelProvider, job: dict[str, Any], cv: dict[str, Any], base: dict[str, Any], facts: list[Fact]) -> dict[str, Any] | None:
    """Compatibility wrapper: the document, or None when the template should be used."""
    return try_model_cover_letter(provider, job, cv, base, facts)[0]


def try_model_cover_letter(provider: ModelProvider, job: dict[str, Any], cv: dict[str, Any], base: dict[str, Any], facts: list[Fact]) -> tuple[dict[str, Any] | None, str | None]:
    """Ask a model to rephrase the template using ONLY the evidenced statements.
    Returns (document, None), or (None, reason) when the template must be used. The reason is one of
    timeout, provider_error, malformed_output, failed_validation: a fallback is never silent."""
    # Models mistype opaque 16-character fact ids (observed live: "unknown_ref"), so they cite short
    # labels S1..Sn which are mapped back to the real ids here; an unknown label stays unknown and is
    # rejected by validation.
    label_ids = {f"S{i + 1}": st["evidence_refs"] for i, st in enumerate(base["statements"])}
    allowed = [{"ref": f"S{i + 1}", "text": st["text"]} for i, st in enumerate(base["statements"])]
    system = ("You write concise, honest cover letters. Use ONLY the candidate statements provided, each may be "
              "rephrased but not extended. Do not add skills, employers, numbers, dates or achievements. "
              "Return JSON: {\"paragraphs\": [str], \"statements\": [{\"text\": str, \"evidence_refs\": [ref]}]} where every "
              "sentence about the candidate appears in statements with the ref labels (such as \"S1\") of the statement it is based on. "
              "Do not write a greeting, an opening sentence about the role, or a sign-off: they are added for you.")
    user = (f"Role: {job['title']} at {job['company']}\nCandidate statements (trusted): {json.dumps(allowed)}\n"
            + data_block("JOB DESCRIPTION", (job.get("description") or "")[:6000]))
    try:
        raw = provider.complete(system, user)
    except httpx.TimeoutException:
        return None, "timeout"
    except Exception:  # noqa: BLE001 - connection refused, 5xx, anything else the provider raises
        return None, "provider_error"
    try:
        m = re.search(r"\{.*\}", raw, re.S)
        data = json.loads(m.group(0) if m else raw)
        paragraphs = [str(p) for p in data["paragraphs"]][:8]
        statements = [{"text": str(s["text"])[:2000],
                       "evidence_refs": [i for r in s["evidence_refs"][:20] for i in label_ids.get(str(r).strip(), [str(r)])]}
                      for s in data["statements"]]
    except Exception:  # noqa: BLE001 - any malformed output falls back to the template
        return None, "malformed_output"
    # The model writes the body only; the salutation, opening and sign-off stay deterministic.
    who = _fact_of(facts, "identity")
    # The template's honest gap sentence is deterministic and evidence-safe, so it is kept verbatim.
    gap = [ln for ln in base["content"].splitlines() if "does not show" in ln]
    content = (f"Dear Hiring Team,\n\nI am applying for the {job['title']} role at {job['company']}.\n\n"
               + "\n\n".join(paragraphs + gap).strip() + f"\n\nKind regards,\n{who.value if who else ''}\n")
    doc = dict(base, content=content, statements=statements)
    issues = validate_document(doc, facts)
    if issues:
        # Codes only, never model text: the output may quote candidate data.
        log.warning("model cover letter rejected by evidence validation: %s", ", ".join(sorted({i["code"] for i in issues})))
        return None, "failed_validation"
    return doc, None


# ------------------------------------------------------------------ validation

_NUM = re.compile(r"\d+(?:[.,]\d+)?%?")


def validate_document(doc: dict[str, Any], facts: list[Fact]) -> list[dict[str, str]]:
    """Evidence checks: statements cite known facts; skills and numbers in a statement appear
    in its cited evidence; no skill in the whole document lacks candidate evidence (except in
    the explicit gap sentence of a cover letter)."""
    by_id = {f.fact_id: f for f in facts}
    evidence_skills = skills_with_evidence(facts)
    issues: list[dict[str, str]] = []
    kind = doc["kind"]
    for st in doc["statements"]:
        refs = st["evidence_refs"]
        if not refs:
            issues.append({"code": "no_evidence", "message": f"statement has no evidence: {st['text'][:80]}", "document_kind": kind})
            continue
        unknown = [r for r in refs if r not in by_id]
        if unknown:
            issues.append({"code": "unknown_ref", "message": f"unknown evidence {unknown}", "document_kind": kind})
            continue
        cited = " ".join(by_id[r].value for r in refs)
        cited_skills = find_skills(cited) | {by_id[r].value for r in refs if by_id[r].category in {"skill", "technology"}}
        for s in find_skills(st["text"]):
            if s not in cited_skills:
                issues.append({"code": "unsupported_skill", "message": f"'{s}' not in cited evidence: {st['text'][:80]}", "document_kind": kind})
        for n in _NUM.findall(st["text"]):
            if n not in cited:
                issues.append({"code": "unsupported_number", "message": f"'{n}' not in cited evidence: {st['text'][:80]}", "document_kind": kind})
    gap_lines = [ln for ln in doc["content"].splitlines() if "does not show" in ln]
    body = "\n".join(ln for ln in doc["content"].splitlines() if ln not in gap_lines and not ln.lower().startswith("i am applying for"))
    for s in find_skills(body):
        if s not in evidence_skills:
            issues.append({"code": "unevidenced_skill", "message": f"document mentions '{s}' without candidate evidence", "document_kind": kind})
    return issues


# ------------------------------------------------------------------ answers

_ANSWER_KEYS = [
    (re.compile(r"notice", re.I), "notice_period"),
    (re.compile(r"visa|sponsor|work (authori[sz]ation|permit)|right to work|citizen", re.I), "work_authorization"),
    (re.compile(r"salary|compensation|expected (pay|package)", re.I), "salary_expectation"),
    (re.compile(r"start date|available to start|availability", re.I), "availability"),
    (re.compile(r"relocat", re.I), "relocation"),
]


def _contact(contacts: list[dict[str, Any]], *kinds: str) -> str | None:
    return next((str(c["value"]).strip() for c in contacts if str(c.get("kind", "")).lower() in kinds and str(c.get("value", "")).strip()), None)


def _name_parts(full_name: str) -> tuple[str, str] | None:
    """First/last only when the full name is exactly two words. Longer names (every Vietnamese
    name has three or more) are ambiguous about which part a form calls "first", so they are
    left for the candidate rather than guessed."""
    parts = full_name.split()
    return (parts[0], parts[1]) if len(parts) == 2 else None


def answer_questions(questions: list[dict[str, Any]], candidate: dict[str, Any], facts: list[Fact]) -> list[dict[str, Any]]:
    standing = {k.strip().lower(): v for k, v in ((candidate.get("preferences") or {}).get("answers") or {}).items()}
    contacts = ((candidate.get("profile") or {}).get("data") or {}).get("contacts") or []
    ident = next((f for f in facts if f.category == "identity"), None)
    names = _name_parts(ident.value) if ident else None
    # Standard identity fields every board asks for, answered from the profile the candidate wrote.
    identity = {
        "email": _contact(contacts, "email"),
        "phone": _contact(contacts, "phone", "tel", "mobile"),
        "first_name": names[0] if names else None,
        "last_name": names[1] if names else None,
    }
    out = []
    for q in questions:
        label = q.get("label", "")
        ans: dict[str, Any] = {"question_id": q["id"], "status": "unknown", "answer": None, "evidence_refs": [], "source": "none"}
        key = next((k for pat, k in _ANSWER_KEYS if pat.search(label)), None)
        direct = standing.get(label.strip().lower())
        if direct:
            ans.update(status="answered", answer=direct, source="candidate_preference")
        elif key and standing.get(key):
            ans.update(status="answered", answer=standing[key], source="candidate_preference")
        elif identity.get(str(q.get("id"))) and ident:
            ans.update(status="answered", answer=identity[str(q["id"])], source="candidate_fact", evidence_refs=[ident.fact_id])
        elif re.search(r"linkedin", label, re.I):
            link = next((c["value"] for c in contacts if c.get("kind") == "linkedin" and c.get("value")), None)
            fact = next((f for f in facts if f.category == "identity"), None)
            if link and fact:
                ans.update(status="answered", answer=link, source="candidate_fact", evidence_refs=[fact.fact_id])
        out.append(ans)
    return out


# ------------------------------------------------------------------ entry point

def prepare(payload: dict[str, Any], provider: ModelProvider | None = None) -> dict[str, Any]:
    provider = provider or NullProvider()
    job, cand = payload["job"], payload["candidate"]
    facts = extract_facts(cand)
    cv, selected = select_cv(cand["cvs"], job)
    docs = [tailor(cv, job, facts)]
    letter = template_cover_letter(job, cv, facts, payload.get("match"))
    used_model, fallback = False, None
    if not isinstance(provider, NullProvider):
        better, fallback = try_model_cover_letter(provider, job, cv, letter, facts)
        if better is not None:
            letter, used_model = better, True
        else:
            log.warning("model cover letter not used (%s) for job %s; template used instead", fallback, job.get("job_id"))
    docs.append(letter)
    issues = [i for d in docs for i in validate_document(d, facts)]
    return {
        "application_id": payload["application_id"],
        "selected_cv": selected,
        "documents": docs,
        "answers": answer_questions(payload.get("questions") or [], cand, facts),
        "validation": {"passed": not issues, "issues": issues},
        "engine": {"name": ENGINE_NAME, "version": ENGINE_VERSION,
                   "model_provider": provider.name if used_model else None,
                   "model_id": provider.model_id if used_model else None,
                   "prompt_version": PROMPT_VERSION if used_model else ("template.v1" + (f" (model fallback: {fallback})" if fallback else ""))},
    }

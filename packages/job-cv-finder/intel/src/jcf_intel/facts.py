"""Candidate fact extraction with evidence locations.

Facts come only from the Ruxt snapshot: the structured profile (CvProfileProps) and the CV
Markdown at its exact revision. Every fact names its source document + version and where in
it the evidence sits. Nothing is inferred beyond simple, labelled derivations (years of
experience from stated date ranges), which are marked verification_status="derived".
"""

from __future__ import annotations

import hashlib
import re
from dataclasses import asdict, dataclass
from datetime import date
from typing import Any

from .lexicon import canonical, find_skills


@dataclass(frozen=True)
class Fact:
    fact_id: str
    category: str
    value: str
    source_document_id: str
    source_version_id: str
    evidence_location: str
    verification_status: str
    confidence: float

    def as_dict(self) -> dict[str, Any]:
        return asdict(self)


def _fid(category: str, value: str, doc: str, loc: str) -> str:
    h = hashlib.sha256(f"{category}\x00{value}\x00{doc}\x00{loc}".encode()).hexdigest()
    return "f_" + h[:16]


def _mk(category: str, value: str, doc: str, ver: str, loc: str, status: str = "candidate_stated", conf: float = 1.0) -> Fact:
    value = value.strip()[:1000]
    return Fact(_fid(category, value, doc, loc), category, value, doc, ver, loc[:200], status, conf)


_MONTHS = {m: i for i, m in enumerate(["jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"], 1)}


def parse_month(s: str, today: date) -> date | None:
    s = s.strip().lower()
    if s in {"present", "now", "current", "hiện tại", "nay"}:
        return today
    m = re.match(r"^(\d{4})[-/.](\d{1,2})", s)
    if m:
        return date(int(m.group(1)), max(1, min(12, int(m.group(2)))), 1)
    m = re.match(r"^(\d{1,2})[-/.](\d{4})$", s)
    if m:
        return date(int(m.group(2)), max(1, min(12, int(m.group(1)))), 1)
    m = re.match(r"^([a-z]{3})[a-z]*\.?\s+(\d{4})$", s)
    if m and m.group(1) in _MONTHS:
        return date(int(m.group(2)), _MONTHS[m.group(1)], 1)
    m = re.match(r"^(\d{4})$", s)
    if m:
        return date(int(m.group(1)), 1, 1)
    return None


def extract_facts(snapshot: dict[str, Any], today: date | None = None) -> list[Fact]:
    today = today or date.today()
    facts: list[Fact] = []
    prof = snapshot["profile"]
    pdoc, pver = "profile:" + prof["profile_id"], prof["updated_at"]
    data = prof.get("data") or {}

    ident = data.get("identity") or {}
    if ident.get("fullName"):
        facts.append(_mk("identity", ident["fullName"], pdoc, pver, "identity.fullName"))
    if ident.get("location"):
        facts.append(_mk("location", ident["location"], pdoc, pver, "identity.location"))
    if ident.get("headline"):
        facts.append(_mk("job_title", ident["headline"], pdoc, pver, "identity.headline"))

    months_total = 0
    for i, exp in enumerate(data.get("experiences") or []):
        loc = f"experiences[{i}]"
        title, company = (exp.get("title") or "").strip(), (exp.get("company") or "").strip()
        if title and company:
            facts.append(_mk("employment", f"{title} at {company}", pdoc, pver, loc))
        if title:
            facts.append(_mk("job_title", title, pdoc, pver, loc + ".title"))
        start, end = parse_month(exp.get("start") or "", today), parse_month(exp.get("end") or "present", today)
        if start and end and end >= start:
            months_total += (end.year - start.year) * 12 + end.month - start.month
        for j, h in enumerate(exp.get("highlights") or []):
            if h.strip():
                facts.append(_mk("responsibility", h, pdoc, pver, f"{loc}.highlights[{j}]"))
                for s in sorted(find_skills(h)):
                    facts.append(_mk("technology", s, pdoc, pver, f"{loc}.highlights[{j}]"))
    if months_total > 0:
        facts.append(_mk("years_experience", f"{months_total // 12}", pdoc, pver, "experiences[*] date ranges", "derived", 0.8))

    for i, grp in enumerate(data.get("skills") or []):
        for j, sk in enumerate(grp.get("skills") or []):
            if sk.strip():
                facts.append(_mk("skill", canonical(sk) or sk.strip(), pdoc, pver, f"skills[{i}].skills[{j}]"))
    for i, c in enumerate(data.get("certifications") or []):
        if (c.get("name") or "").strip():
            facts.append(_mk("certification", c["name"], pdoc, pver, f"certifications[{i}]"))
    for i, ed in enumerate(data.get("education") or []):
        v = " ".join(x for x in [ed.get("degree", ""), ed.get("school", "")] if x).strip()
        if v:
            facts.append(_mk("education", v, pdoc, pver, f"education[{i}]"))
    for i, pr in enumerate(data.get("projects") or []):
        if (pr.get("name") or "").strip():
            facts.append(_mk("project", pr["name"] + (": " + pr["description"] if pr.get("description") else ""), pdoc, pver, f"projects[{i}]"))
        for t in pr.get("technologies") or []:
            facts.append(_mk("technology", canonical(t) or t, pdoc, pver, f"projects[{i}].technologies"))
    for i, lang in enumerate(data.get("languages") or []):
        if lang.strip():
            facts.append(_mk("language", lang, pdoc, pver, f"languages[{i}]"))

    for cv in snapshot.get("cvs") or []:
        cdoc, cver = "cv:" + cv["cv_id"], str(cv["revision"])
        for n, line in enumerate(cv["markdown"].splitlines(), 1):
            text = line.strip()
            bullet = re.match(r"^([-*+]|\d+\.)\s+(.*)$", text)
            if not bullet:
                continue
            content = bullet.group(2).strip()
            if len(content) < 3:
                continue
            facts.append(_mk("achievement", content, cdoc, cver, f"L{n}"))
            for s in sorted(find_skills(content)):
                facts.append(_mk("technology", s, cdoc, cver, f"L{n}"))

    seen: set[str] = set()
    unique = []
    for f in facts:
        if f.fact_id not in seen:
            seen.add(f.fact_id)
            unique.append(f)
    return unique


def skills_with_evidence(facts: list[Fact]) -> dict[str, list[str]]:
    """Canonical skill -> fact ids that evidence it."""
    out: dict[str, list[str]] = {}
    for f in facts:
        if f.category in {"skill", "technology"}:
            out.setdefault(canonical(f.value) or f.value, []).append(f.fact_id)
        elif f.category in {"responsibility", "achievement"}:
            for s in find_skills(f.value):
                out.setdefault(s, []).append(f.fact_id)
    return out

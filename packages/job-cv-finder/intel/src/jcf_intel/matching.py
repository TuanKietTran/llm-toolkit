"""Explainable job matching against evidenced candidate facts.

The score is a weighted sum over dimensions whose status is known. Unknown dimensions do not
count for or against the score but lower confidence; partial job data (email alerts without a
description) therefore yields low-confidence results instead of guessed ones.
"""

from __future__ import annotations

import re
from typing import Any

from . import ENGINE_NAME, ENGINE_VERSION
from .facts import Fact, extract_facts, skills_with_evidence
from .lexicon import canonical, find_skills

WEIGHTS = {
    "required_skills": 0.40,
    "preferred_skills": 0.10,
    "experience": 0.15,
    "seniority": 0.05,
    "location": 0.10,
    "work_mode": 0.10,
    "compensation": 0.05,
    "career_preferences": 0.05,
}


def _dim(status: str, score: float | None, note: str = "") -> dict[str, Any]:
    d: dict[str, Any] = {"status": status, "score": None if score is None else round(score, 3)}
    if note:
        d["note"] = note[:500]
    return d


def _req(text: str, kind: str, refs: list[str], note: str = "") -> dict[str, Any]:
    r: dict[str, Any] = {"requirement": text[:300], "kind": kind, "evidence_refs": refs[:20]}
    if note:
        r["note"] = note[:500]
    return r


def _fold(s: str) -> str:
    import unicodedata

    s = unicodedata.normalize("NFD", s)
    s = "".join(c for c in s if unicodedata.category(c) != "Mn")
    return s.replace("đ", "d").replace("Đ", "D").lower()


def _job_skills(job: dict[str, Any]) -> tuple[list[str], list[str]]:
    req = [canonical(s) or s for s in job.get("required_skills") or []]
    pref = [canonical(s) or s for s in job.get("preferred_skills") or []]
    if not req and job.get("title"):
        # Email alerts often carry the stack only in the title ("Backend Engineer (Python, SQL)").
        req = sorted(find_skills(job["title"]))
    return req, [p for p in pref if p not in req]


def match(job: dict[str, Any], candidate: dict[str, Any], snapshot_id: str) -> dict[str, Any]:
    facts = extract_facts(candidate)
    evidence = skills_with_evidence(facts)
    prefs = candidate.get("preferences") or {}
    matched, missing, uncertain = [], [], []
    dims: dict[str, dict[str, Any]] = {}

    req, pref = _job_skills(job)
    if req:
        hit = [s for s in req if s in evidence]
        for s in req:
            if s in evidence:
                matched.append(_req(s, "required_skill", evidence[s]))
            else:
                missing.append(_req(s, "required_skill", [], "no candidate evidence"))
        cov = len(hit) / len(req)
        dims["required_skills"] = _dim("met" if cov >= 0.99 else "partial" if cov > 0 else "unmet", cov, f"{len(hit)}/{len(req)} evidenced")
    else:
        dims["required_skills"] = _dim("unknown", None, "job lists no recognisable required skills")
        uncertain.append(_req("required skills not stated", "required_skill", [], "partial job data"))
    if pref:
        hit = [s for s in pref if s in evidence]
        for s in pref:
            (matched if s in evidence else missing).append(_req(s, "preferred_skill", evidence.get(s, [])))
        dims["preferred_skills"] = _dim("met" if len(hit) == len(pref) else "partial" if hit else "unmet", len(hit) / len(pref))
    else:
        dims["preferred_skills"] = _dim("not_applicable", None)

    # Experience: "N+ years" vs derived years from the profile's own date ranges.
    years_fact = next((f for f in facts if f.category == "years_experience"), None)
    m = re.match(r"(\d+)", job.get("experience_requirement") or "")
    if m and years_fact:
        need, have = int(m.group(1)), int(years_fact.value)
        ok = have >= need
        dims["experience"] = _dim("met" if ok else "partial" if have >= need - 1 else "unmet", 1.0 if ok else max(0.0, have / need), f"{have} vs {need}+ years")
        (matched if ok else missing).append(_req(f"{need}+ years experience", "experience", [years_fact.fact_id] if ok else []))
    elif m:
        dims["experience"] = _dim("unknown", None, "candidate experience dates not in profile")
        uncertain.append(_req(f"{m.group(1)}+ years experience", "experience", [], "cannot compute from profile"))
    else:
        dims["experience"] = _dim("not_applicable", None)

    # Seniority alignment from titles only (no guessing beyond words present).
    title = _fold(job.get("title", ""))
    cand_titles = " ".join(_fold(f.value) for f in facts if f.category == "job_title")
    senior_job = any(w in title for w in ("senior", "lead", "principal", "staff"))
    junior_job = any(w in title for w in ("junior", "intern", "fresher"))
    senior_cand = any(w in cand_titles for w in ("senior", "lead", "principal", "staff"))
    if senior_job or junior_job:
        if senior_job and not senior_cand and cand_titles:
            dims["seniority"] = _dim("partial", 0.5, "job is senior; candidate titles do not say senior")
        elif junior_job and senior_cand:
            dims["seniority"] = _dim("partial", 0.5, "job is junior; candidate is senior")
        elif not cand_titles:
            dims["seniority"] = _dim("unknown", None)
        else:
            dims["seniority"] = _dim("met", 1.0)
    else:
        dims["seniority"] = _dim("not_applicable", None)

    # Location & work mode against stated preferences.
    want_locs = [_fold(x) for x in prefs.get("locations") or []]
    job_loc = _fold(job.get("location") or "")
    work_mode = job.get("work_mode") or "unknown"
    want_modes = prefs.get("work_modes") or []
    if work_mode == "remote" and "remote" in want_modes:
        dims["location"] = _dim("met", 1.0, "remote role")
    elif not job_loc or not want_locs:
        dims["location"] = _dim("unknown", None)
    elif any(w and (w in job_loc or job_loc in w) for w in want_locs):
        dims["location"] = _dim("met", 1.0)
        matched.append(_req(job.get("location", ""), "location", []))
    else:
        dims["location"] = _dim("unmet", 0.0, "outside preferred locations")
        missing.append(_req(job.get("location", ""), "location", [], "not a preferred location"))
    if work_mode == "unknown" or not want_modes:
        dims["work_mode"] = _dim("unknown", None)
    else:
        ok = work_mode in want_modes
        dims["work_mode"] = _dim("met" if ok else "unmet", 1.0 if ok else 0.0, work_mode)
        if not ok:
            missing.append(_req(f"{work_mode} work", "work_mode", [], "not a preferred work mode"))

    smin = prefs.get("salary_min")
    jmax, jcur = job.get("salary_max"), (job.get("salary_currency") or "").upper()
    pcur = (prefs.get("salary_currency") or "").upper()
    if smin and jmax and jcur and pcur and jcur == pcur:
        ok = jmax >= smin
        dims["compensation"] = _dim("met" if ok else "unmet", 1.0 if ok else 0.0, f"up to {jmax} {jcur} vs minimum {smin}")
    else:
        dims["compensation"] = _dim("unknown", None)

    excluded = [k for k in prefs.get("excluded_keywords") or [] if _fold(k) and _fold(k) in title]
    targets = [t for t in prefs.get("target_roles") or [] if _fold(t) and any(w in title for w in _fold(t).split() if len(w) > 3)]
    if excluded:
        dims["career_preferences"] = _dim("unmet", 0.0, "excluded keyword: " + ", ".join(excluded))
        missing.append(_req("excluded: " + ", ".join(excluded), "career_preference", []))
    elif prefs.get("target_roles"):
        dims["career_preferences"] = _dim("met" if targets else "partial", 1.0 if targets else 0.5)
    else:
        dims["career_preferences"] = _dim("unknown", None)

    known = {k: d for k, d in dims.items() if d["score"] is not None}
    weight = sum(WEIGHTS[k] for k in known)
    score = sum(WEIGHTS[k] * d["score"] for k, d in known.items()) / weight if weight else 0.0
    confidence = weight / sum(WEIGHTS.values())
    if job.get("completeness") == "partial":
        confidence *= 0.6

    hard_fail = excluded or dims["location"]["status"] == "unmet" or dims["work_mode"]["status"] == "unmet"
    eligibility = "ineligible" if hard_fail else ("uncertain" if confidence < 0.5 else "eligible")
    if hard_fail or score < 0.35:
        rec = "no_match"
    elif score >= 0.8 and confidence >= 0.6:
        rec = "strong_match"
    elif score >= 0.6:
        rec = "match"
    else:
        rec = "weak_match"

    return {
        "job_id": job["job_id"],
        "snapshot_id": snapshot_id,
        "match_score": round(score, 4),
        "eligibility": eligibility,
        "confidence": round(confidence, 4),
        "dimensions": dims,
        "matched_requirements": matched,
        "missing_requirements": missing,
        "uncertain_requirements": uncertain,
        "facts": [f.as_dict() for f in facts],
        "recommendation": rec,
        "engine": {"name": ENGINE_NAME, "version": ENGINE_VERSION, "model_provider": None, "model_id": None, "prompt_version": None},
    }


def facts_for(candidate: dict[str, Any]) -> list[Fact]:
    return extract_facts(candidate)

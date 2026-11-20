import copy
import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator, FormatChecker

CONTRACTS = Path(__file__).resolve().parents[2] / "core" / "contracts"

SNAPSHOT = {
    "schema_version": 1,
    "source": {"system": "ruxt", "exported_at": "2026-11-20T00:00:00Z"},
    "profile": {
        "profile_id": "profile-1",
        "updated_at": "2026-11-19T00:00:00Z",
        "data": {
            "version": 1,
            "identity": {"fullName": "Test Candidate", "headline": "Senior Backend Engineer", "summary": "", "location": "Ho Chi Minh City"},
            "contacts": [{"kind": "email", "label": "Email", "value": "candidate@example.test"},
                         {"kind": "linkedin", "label": "LinkedIn", "value": "https://www.linkedin.com/in/example"}],
            "experiences": [
                {"title": "Senior Backend Engineer", "company": "Example Pay", "location": "HCMC", "start": "2021-03", "end": "present",
                 "highlights": ["Built payment APIs in Go on PostgreSQL serving 2M requests/day", "Ran services on Docker and Kubernetes"]},
                {"title": "Software Engineer", "company": "Sample Corp", "location": "HCMC", "start": "2018-06", "end": "2021-02",
                 "highlights": ["Maintained Python data pipelines"]},
            ],
            "skills": [{"name": "Backend", "skills": ["Golang", "PostgreSQL", "Docker", "Python"]}],
            "certifications": [], "education": [{"degree": "BSc Computer Science", "school": "Example University", "location": "", "start": "", "end": "", "details": ""}],
            "projects": [], "languages": ["English", "Vietnamese"],
        },
    },
    "cvs": [
        {"cv_id": "cv-backend", "revision": 7, "title": "Backend CV", "updated_at": "2026-11-18T00:00:00Z",
         "markdown": "# Test Candidate\n\n## Experience\n- Maintained Python data pipelines\n- Built payment APIs in Go on PostgreSQL serving 2M requests/day\n- Ran services on Docker and Kubernetes\n"},
        {"cv_id": "cv-frontend", "revision": 2, "title": "Frontend CV", "updated_at": "2026-11-01T00:00:00Z",
         "markdown": "# Test Candidate\n\n## Experience\n- Built dashboards in TypeScript and React\n"},
    ],
    "preferences": {"locations": ["Ho Chi Minh City"], "work_modes": ["hybrid", "remote"], "target_roles": ["Backend Engineer"],
                    "excluded_keywords": ["QA"], "answers": {"notice_period": "One month"}},
}

JOB = {
    "job_id": "job_1", "source_id": "mockats:demo", "company": "Acme", "title": "Senior Backend Engineer (Go)",
    "description": "Requirements: 4+ years Golang, PostgreSQL, Docker. Nice to have: Kubernetes.",
    "location": "Ho Chi Minh City", "work_mode": "hybrid", "required_skills": ["Docker", "Go", "PostgreSQL"],
    "preferred_skills": ["Kubernetes"], "experience_requirement": "4+ years", "completeness": "full",
    "salary_max": None, "salary_currency": "",
}


@pytest.fixture
def snapshot():
    return copy.deepcopy(SNAPSHOT)


@pytest.fixture
def job():
    return copy.deepcopy(JOB)


def validate(name, obj):
    schema = json.loads((CONTRACTS / name).read_text(encoding="utf-8"))
    errors = list(Draft202012Validator(schema, format_checker=FormatChecker()).iter_errors(obj))
    assert not errors, [f"{list(e.path)}: {e.message}" for e in errors]


@pytest.fixture
def check():
    return validate

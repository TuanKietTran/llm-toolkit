"""Skill lexicon shared by fact extraction, matching and generation checks.

Kept in step with the Go normalizer's lexicon (core/internal/normalize/skills.go): Go uses
it to extract job requirements, Python to find the same skills in candidate evidence.
"""

from __future__ import annotations

import re

SKILLS: dict[str, list[str]] = {
    "Go": ["golang", "go"],
    "Python": ["python"],
    "TypeScript": ["typescript", "ts"],
    "JavaScript": ["javascript", "js"],
    "Node.js": ["node.js", "nodejs", "node"],
    "NestJS": ["nestjs"],
    "React": ["react", "reactjs"],
    "Vue": ["vue", "vuejs", "nuxt"],
    "Java": ["java"],
    "C#": ["c#", ".net", "dotnet"],
    "Rust": ["rust"],
    "C++": ["c++"],
    "SQL": ["sql"],
    "PostgreSQL": ["postgresql", "postgres"],
    "MySQL": ["mysql"],
    "MongoDB": ["mongodb", "mongo"],
    "Redis": ["redis"],
    "Kafka": ["kafka"],
    "Docker": ["docker"],
    "Kubernetes": ["kubernetes", "k8s"],
    "AWS": ["aws", "amazon web services"],
    "GCP": ["gcp", "google cloud"],
    "Azure": ["azure"],
    "Terraform": ["terraform"],
    "CI/CD": ["ci/cd", "cicd", "github actions", "gitlab ci"],
    "Linux": ["linux"],
    "gRPC": ["grpc"],
    "GraphQL": ["graphql"],
    "LLM": ["llm", "llms", "large language model", "generative ai", "genai"],
    "RAG": ["rag", "retrieval augmented", "retrieval-augmented"],
    "Machine Learning": ["machine learning", "ml"],
    "Spark": ["spark", "pyspark"],
    "Airflow": ["airflow"],
    "dbt": ["dbt"],
    "Odoo": ["odoo"],
    "Microservices": ["microservices", "microservice"],
    "Swift": ["swift"],
    "Leadership": ["leadership", "team lead", "tech lead"],
}

# "go" and similar short words are ambiguous in prose ("go live"); only accept them when
# the surrounding text looks technical (comma lists, "Go," "Go/", or capitalised "Go").
_AMBIGUOUS = {"go", "ts", "js", "node", "rust", "swift", "spark", "ml"}

_ALIASES: list[tuple[str, re.Pattern[str], str]] = []
for canon, spellings in SKILLS.items():
    for sp in spellings:
        pat = re.compile(r"(?<![A-Za-z0-9])" + re.escape(sp) + r"(?![A-Za-z0-9+#])", re.IGNORECASE)
        _ALIASES.append((canon, pat, sp))


def canonical(name: str) -> str | None:
    """Map a free-form skill name ("ReactJS", "Golang") to its canonical lexicon entry."""
    n = name.strip().lower()
    for canon, spellings in SKILLS.items():
        if n == canon.lower() or n in spellings:
            return canon
    return None


def find_skills(text: str) -> set[str]:
    """Skills mentioned in text. Ambiguous short words need a technical context."""
    found: set[str] = set()
    for canon, pat, sp in _ALIASES:
        for m in pat.finditer(text):
            if sp in _AMBIGUOUS:
                token = text[m.start():m.end()]
                after = text[m.end():m.end() + 1]
                before = text[max(0, m.start() - 2):m.start()]
                technical = (token[0].isupper() and sp not in {"spark", "swift", "rust"}) or after in {",", "/", ")"} or before.endswith(("(", "/", ", "))
                if sp in {"spark", "swift", "rust"}:
                    technical = token[0].isupper()
                if not technical:
                    continue
            found.add(canon)
            break
    return found

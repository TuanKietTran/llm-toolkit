# Job CV Finder intelligence worker (Python). Build context: packages/job-cv-finder
FROM python:3.13.18-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
RUN useradd --system --uid 10001 --no-create-home jcf
WORKDIR /app
COPY intel/requirements.lock ./
RUN pip install --require-hashes -r requirements.lock
COPY intel/src ./src
COPY core/contracts ./contracts
ENV PYTHONPATH=/app/src JCF_CONTRACTS_DIR=/app/contracts
USER 10001:10001
ENTRYPOINT ["python", "-m", "jcf_intel"]

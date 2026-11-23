# Job CV Finder intelligence worker (Python). Build context: packages/job-cv-finder
FROM python:3.13.18-slim-bookworm
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1
RUN useradd --system --uid 10001 --no-create-home jcf
WORKDIR /app
COPY intel/requirements.lock ./
# pip and setuptools are only needed to install; removing them also removes their vendored copies of
# urllib3/msgpack that vulnerability scanners flag and that the worker never uses.
RUN pip install --require-hashes -r requirements.lock \n && pip uninstall -y pip setuptools wheel \n && rm -rf /usr/local/lib/python3.13/ensurepip /usr/local/lib/python3.13/site-packages/pip*
COPY intel/src ./src
COPY core/contracts ./contracts
ENV PYTHONPATH=/app/src JCF_CONTRACTS_DIR=/app/contracts
USER 10001:10001
ENTRYPOINT ["python", "-m", "jcf_intel"]

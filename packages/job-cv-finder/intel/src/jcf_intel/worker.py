"""Worker loop: lease a task from the Go core, compute, validate against the shared JSON
Schema contract, and report back. The worker holds no state; a crash just lets the lease
expire and the core redelivers the task.

    JCF_CORE_URL=http://127.0.0.1:8790 JCF_WORKER_TOKEN=... python -m jcf_intel.worker
"""

from __future__ import annotations

import argparse
import json
import logging
import os
import socket
import sys
import time
from pathlib import Path
from typing import Any, Callable

import httpx
from jsonschema import Draft202012Validator, FormatChecker

from . import llm
from .matching import match
from .prepare import prepare

log = logging.getLogger("jcf_intel")

CONTRACTS = Path(os.environ.get("JCF_CONTRACTS_DIR", Path(__file__).resolve().parents[3] / "core" / "contracts"))


def _validator(name: str) -> Draft202012Validator:
    schema = json.loads((CONTRACTS / name).read_text(encoding="utf-8"))
    return Draft202012Validator(schema, format_checker=FormatChecker())


class ContractError(ValueError):
    pass


def handle(task: dict[str, Any], provider: llm.ModelProvider) -> dict[str, Any]:
    payload = task["payload"]
    if task["kind"] == "intel.match":
        result, schema = match(payload["job"], payload["candidate"], payload["snapshot_id"]), "match-result.schema.json"
    elif task["kind"] == "intel.prepare":
        result, schema = prepare(payload, provider), "prepare-result.schema.json"
    else:
        raise ContractError(f"unknown task kind {task['kind']}")
    errors = sorted(_validator(schema).iter_errors(result), key=lambda e: list(e.path))
    if errors:
        raise ContractError("; ".join(f"{list(e.path)}: {e.message}" for e in errors[:5]))
    return result


class Client:
    def __init__(self, base: str, token: str, worker: str, http: httpx.Client | None = None):
        self.base, self.worker = base.rstrip("/"), worker
        self.http = http or httpx.Client(timeout=30)
        self.headers = {"Authorization": f"Bearer {token}"}

    def lease(self, kinds: list[str], seconds: int = 300) -> dict[str, Any] | None:
        r = self.http.post(f"{self.base}/internal/v1/tasks/lease", headers=self.headers,
                           json={"kinds": kinds, "worker": self.worker, "lease_seconds": seconds})
        if r.status_code == 204:
            return None
        r.raise_for_status()
        return r.json()

    def complete(self, task_id: str, result: dict[str, Any]) -> httpx.Response:
        return self.http.post(f"{self.base}/internal/v1/tasks/{task_id}/complete", headers=self.headers,
                              json={"worker": self.worker, "result": result})

    def fail(self, task_id: str, error: str, retryable: bool) -> httpx.Response:
        return self.http.post(f"{self.base}/internal/v1/tasks/{task_id}/fail", headers=self.headers,
                              json={"worker": self.worker, "error": error[:4000], "retryable": retryable})


def run_once(client: Client, provider: llm.ModelProvider, kinds: list[str]) -> bool:
    """Process at most one task. Returns False when the queue was empty."""
    task = client.lease(kinds)
    if task is None:
        return False
    try:
        result = handle(task, provider)
    except ContractError as e:
        log.error("contract violation on %s: %s", task["id"], e)
        client.fail(task["id"], f"worker contract violation: {e}", retryable=False)
        return True
    except Exception as e:  # noqa: BLE001 - report, let the core decide on retry
        log.exception("task %s failed", task["id"])
        client.fail(task["id"], f"{type(e).__name__}: {e}", retryable=True)
        return True
    r = client.complete(task["id"], result)
    if r.status_code >= 400:
        log.error("core rejected result for %s: %s %s", task["id"], r.status_code, r.text[:500])
    else:
        log.info("completed %s %s", task["kind"], task["id"])
    return True


def main(argv: list[str] | None = None, sleep: Callable[[float], None] = time.sleep) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--core", default=os.environ.get("JCF_CORE_URL", "http://127.0.0.1:8790"))
    ap.add_argument("--kinds", default="intel.match,intel.prepare")
    ap.add_argument("--once", action="store_true", help="drain the queue and exit")
    args = ap.parse_args(argv)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    logging.getLogger("httpx").setLevel(logging.WARNING)  # idle polling would flood the log
    token = os.environ.get("JCF_WORKER_TOKEN", "")
    if not token:
        print("JCF_WORKER_TOKEN is required", file=sys.stderr)
        return 2
    client = Client(args.core, token, f"intel-{socket.gethostname()}-{os.getpid()}")
    provider = llm.from_env()
    log.info("worker started; model provider=%s model=%s", provider.name, provider.model_id)
    kinds = [k for k in args.kinds.split(",") if k]
    idle = 1.0
    while True:
        try:
            worked = run_once(client, provider, kinds)
        except httpx.HTTPError as e:
            log.warning("core unreachable: %s", e)
            worked = False
        if worked:
            idle = 1.0
            continue
        if args.once:
            return 0
        sleep(idle)
        idle = min(idle * 2, 15.0)


if __name__ == "__main__":
    raise SystemExit(main())

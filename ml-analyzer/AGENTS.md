# ml-analyzer guardrails (FastAPI)

Layout: `app/main.py` (routes), `app/scoring/` (heuristic + LLM backends), `app/coaching/`, `app/config.py`, `app/schemas.py`. Tests live beside the code (`app/test_*.py`) plus `conftest.py`. `train.py`, `training/`, `data/` for offline model work.

## Required gate (`test`)

```sh
cd ml-analyzer
pip install -r requirements.txt -r requirements-dev.txt
pytest
```

## Invariants

- **Offline-capable:** `ML_BACKEND=heuristic` (or unset without an LLM key) must always work — CI and e2e run with no `LLM_API_KEY`. The LLM path (`ML_BACKEND=llm`, `LLM_PROVIDER`, `LLM_MODEL`) must degrade to heuristic on failure, not 5xx.
- **`/healthz` stays dependency-free** — it's the deploy/e2e readiness probe.
- **Private service:** in production only the api/worker call it (Google ID token, `ML_SERVICE_AUTH=google`). Never expose it publicly; keep request auth intact.
- **Scoring changes need evidence:** update the affected `test_*` suites (agreement, eval-vs-LLM, quality, heuristic patterns) in the same PR; don't silently loosen thresholds.
- **Determinism:** heuristic scoring must be deterministic and cheap — no network/LLM calls in the fallback path.

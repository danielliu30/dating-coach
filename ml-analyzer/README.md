# ml-analyzer

FastAPI service that scores a dating-app conversation for engagement /
interestingness. It is fully decoupled from the Go backend — the only coupling
is the HTTP contract below.

```
POST /analyze   → per-segment scores + overall feedback
GET  /healthz   → status, active backend, model version
```

## Run locally

```bash
python -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt
uvicorn app.main:app --reload --port 8000
```

```bash
curl -s localhost:8000/analyze -H 'content-type: application/json' -d '{
  "conversation_id": "demo",
  "platform": "hinge",
  "messages": [
    {"position": 0, "sender": "self",  "body": "hey"},
    {"position": 1, "sender": "match", "body": "hi"},
    {"position": 2, "sender": "self",  "body": "your profile says you bake - what was the last thing you made?"},
    {"position": 3, "sender": "match", "body": "sourdough, badly. it came out like a frisbee"}
  ]
}' | jq
```

## API contract

Request:

| Field | Type | Notes |
| --- | --- | --- |
| `conversation_id` | string | Echo of the backend's row id. |
| `platform` | string | Optional, defaults to `unknown`. |
| `match_name` | string | Optional, used only in the prompt. |
| `messages[]` | array | `position` (0-based), `sender` (`self`\|`match`), `body`, optional `sent_at`. |

Response:

| Field | Type | Notes |
| --- | --- | --- |
| `model_version` | string | Which backend/model produced the scores. Stored with the result. |
| `segments[]` | array | `start_position`, `end_position`, `engagement_score` (0–1), `label` (`engaging`\|`neutral`\|`flat`), `comment`. |
| `overall` | object | `engagement_score`, `summary`, `strengths[]`, `improvements[]`. |

`label` is always derived from the score (≥0.66 engaging, ≥0.4 neutral, else
flat), so it is consistent across backends.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ML_BACKEND` | `llm` | `llm`, `trained` or `heuristic`. |
| `LLM_PROVIDER` | `openai` | `openai` (also any OpenAI-compatible gateway: Azure, Ollama, vLLM) or `anthropic`. |
| `LLM_API_KEY` | – | If unset, the service logs a warning and falls back to the heuristic scorer. |
| `LLM_MODEL` | `gpt-4o-mini` / `claude-3-5-haiku-latest` | Per provider. |
| `LLM_BASE_URL` | provider default | Point at a self-hosted gateway. |
| `LLM_TIMEOUT_SECONDS` | `45` | |
| `ML_MODEL_DIR` | `artifacts/segment-scorer` | Checkpoint used when `ML_BACKEND=trained`. |
| `ML_SEGMENT_SIZE` | `4` | Messages per segment. |

## Backends

`app/scoring/` contains one class per backend behind the `Scorer` ABC
(`analyze(request) -> AnalyzeResponse`). `build_scorer()` picks one from env.

- **`llm.py` (v1, default)** — prompts the LLM with the transcript and the exact
  segment boundaries, parses strict JSON, clamps and re-labels every score. Any
  failure (timeout, bad JSON, rate limit) degrades to the heuristic scorer and
  reports it in `model_version` rather than failing the analysis job.
- **`heuristic.py`** — no network, no model. Length/question/reciprocity signals.
  Used for local dev, CI and as the LLM fallback.
- **`trained.py`** — a fine-tuned checkpoint. torch/transformers are imported
  lazily so the serving image stays small until you actually use it.

## Training your own model

### 1. Collect data

The backend already stores what you need: `conversations` + `messages` hold
submitted transcripts, and `training_examples` holds labels
(`engagement_score`, `segment_labels` JSONB, `reply_received`, `outcome`,
`consented`). The app asks the user, after they read their analysis, what
happened next — that is where `outcome` comes from.

Export a dataset:

```sql
\copy (
  SELECT json_build_object(
    'conversation_id', c.id,
    'user_id',        c.user_id,
    'platform',       c.platform,
    'consented',      t.consented,
    'reply_received', t.reply_received,
    'outcome',        t.outcome,
    'segments',       t.segment_labels,
    'messages',       json_agg(json_build_object(
                        'position', m.position, 'sender', m.sender,
                        'body', m.body, 'sent_at', m.sent_at
                      ) ORDER BY m.position)
  )
  FROM training_examples t
  JOIN conversations c ON c.id = t.conversation_id
  JOIN messages m      ON m.conversation_id = c.id
  WHERE t.consented
  GROUP BY c.id, c.user_id, c.platform, t.consented, t.reply_received, t.outcome, t.segment_labels
) TO 'data/labeled.jsonl';
```

Ground rules: only export `consented` rows, pseudonymise names/handles/links
before they leave the database, and keep an opt-out path that deletes a user's
training rows. `outcome`/`reply_received` are weak labels — cheap and plentiful;
human `segments` labels are scarce and authoritative. Aim for a few thousand
weak-labeled conversations plus a few hundred hand-labeled ones as the eval set,
and keep the eval set user-disjoint from training.

### 2. Label

See [`training/data_schema.md`](training/data_schema.md) for the schema and the
annotation rubric. A cheap bootstrap: run the LLM backend over your archive,
have annotators *correct* the scores rather than produce them from scratch.

### 3. Train

```bash
pip install -r requirements-train.txt
python train.py --data data/labeled.jsonl --dry-run   # dataset stats only
python train.py --data data/labeled.jsonl --out artifacts/segment-scorer
```

Default recipe: DistilBERT with a single-logit head over segment transcripts,
soft-target BCE against the 0–1 score (so serving's `sigmoid(logit)` is on the
same scale as the labels), `mae`/`rmse` on the eval split.

For a generative model that also writes the `comment`/`summary` text, LoRA
fine-tune a small instruct model on `(transcript → the exact JSON the prompt
backend returns)` pairs — add `peft` from `requirements-train.txt`, and have
`TrainedScorer.analyze` parse the generated JSON with the same
`LLMScorer._parse` logic.

### 4. Switch backends

```bash
pip install -r requirements-train.txt   # torch/transformers are not in the serving image
ML_BACKEND=trained ML_MODEL_DIR=artifacts/segment-scorer \
  uvicorn app.main:app --port 8000
```

In Docker, build the image with the training dependencies included:

```bash
docker build --build-arg INSTALL_TRAINING_DEPS=1 -t dating-coach-ml:trained ml-analyzer
```

Nothing else changes: same `/analyze` request and response, so the Go worker and
the app are unaware. `model_version` in each stored `analysis_results` row tells
you which model produced which result, so you can compare the trained model
against the LLM on live traffic before making it the default.

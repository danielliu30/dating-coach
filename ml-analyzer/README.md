# ml-analyzer

FastAPI service with two independent analysis tracks for a dating-app customer.
It is fully decoupled from the Go backend — the only coupling is the HTTP
contract below.

```
POST /analyze          → message track: critique of the customer's own messages
POST /analyze/images   → image track: clarity + focal-point verdict per profile photo
POST /summarize/reviews → coach reviews → recommendation line + named strengths (no stars)
GET  /healthz          → status, active backend, model version
```

Both tracks accept an optional `preferences` string — the customer's own words
about what they are looking for — and tailor their feedback to it. The Go
backend fills it from `users.dating_preferences`.

### The coaching brain

The message track's feedback is authored from one canonical, versioned coaching
philosophy in `app/coaching/brain.py`, built on three pillars (the image and
review tracks have their own prompts and are not yet wired to the brain):

- **AGENCY** — the client drives. Their words, decisions and dating life stay
  theirs; self-awareness is what creates agency. Never prescribe who to date,
  keep or drop; never mind-read the match's motives.
- **FEEDBACK** — judge only the client's own behaviour by what it produced
  (no reply / short reply / engaged reply); hint, never draft; name patterns
  and hand back reflection questions; relate it to their stated preferences.
- **SUPPORT** — meet the client where they are; encourage without flattering;
  honest and direct but non-directive; point to a human coach when that is the
  better help.

The LLM `SYSTEM_PROMPT` is composed from the rendered pillars
(`render_pillars()`), not written by hand, and `BRAIN_VERSION` is appended to
`model_version` (e.g. `llm-openai-gpt-4o+brain-v1`) so every stored result is
traceable to the philosophy revision that produced it.

The model generates its own feedback from this brain. What online forums or
Reddit would say is never a target to match and is never fed into or copied into
feedback; the only place crowd-style advice appears in this repo is as
negative-example fixtures in the tests, proving the gate below rejects that
style rather than imitating it.

Agency compliance is a hard gate, not a prompt preference: `app/coaching/agency.py`
(`enforce_agency`) deterministically scans every piece of model output for
three violations — **drafting** a reply, **mind-reading** the match ("she's not
into you") and **prescribing** the client's dating life ("drop them", "move
on") — while still allowing verbatim `Message N ("...")` citations of the
client's own words. Any hit discards the whole LLM result and falls back to the
heuristic scorer.

### Message track

Only the customer's messages (`sender == "self"`) are evaluated. The match's
messages are context and evidence, never scored. For each customer message the
analysis looks at what came back — nothing, a short reply, or an engaged reply —
and flags the pattern as a hint. It **never drafts what to say**: the customer
drives the conversation, the analyzer only points at what did and did not land.
The LLM backend enforces this by rejecting any completion that reads as a
suggested reply and falling back to the heuristic scorer. (`ML_BACKEND=trained`
is the exception — see the backends section.)

**Product thesis.** The message track is the "Dating Humane" manifesto
(surfaced on the app landing page, `app/src/components/Landing.tsx`) made
concrete. It is built around agency:

- *Coach self-awareness, not just outcomes.* Alongside "did they like me?" the
  analysis asks "did I like them?": `overall.reflection_questions` are open,
  inward questions (did you enjoy this? were you putting in more than they
  were, and did that feel okay?) that never tell the customer what to do.
- *Name patterns as questions.* Recurring behaviour — including reciprocity,
  i.e. investing more energy than the person on the other side — is stated as
  an observation in `overall.patterns` and handed back: "here's a pattern we've
  noticed; is this worth thinking about?"
- *Never draft.* Your words should still be your words. No field ever contains
  a suggested or rewritten message, and the anti-drafting guard covers the new
  fields too.
- *Never diagnose rejection.* Feedback describes what happened (no reply, a
  short reply, an engaged reply) and does not claim to know *why* the match
  pulled back. We can't know that. The heuristic scorer's wording is fixed and
  tested for this. For the LLM backend it is a prompt rule only: the parser
  rejects drafted replies but has no scan for rejection-diagnosis language.

### Image track

Each photo is judged on two things: is it sharp and well lit, and is the
customer unmistakably the focal point. Feedback is tailored to `preferences`.
Without an LLM key the heuristic image scorer reads inline image headers for
resolution and format only; it cannot see who is in the frame, so it returns a
neutral focus score and says so.

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

```bash
curl -s localhost:8000/analyze/images -H 'content-type: application/json' -d '{
  "preferences": "someone outdoorsy who wants something serious",
  "images": [
    {"url": "https://cdn.example/me/hiking.jpg"},
    {"base64": "/9j/4AAQSkZJRg...", "media_type": "image/jpeg"}
  ]
}' | jq
```

## API contract

### `POST /analyze` (messages)

Request:

| Field | Type | Notes |
| --- | --- | --- |
| `conversation_id` | string | Echo of the backend's row id. |
| `platform` | string | Optional, defaults to `unknown`. |
| `match_name` | string | Optional. Used in the prompt, and by the agency gate so that claims or advice naming the match ("Sam isn't into you") are caught like pronoun forms. |
| `messages[]` | array | `position` (0-based), `sender` (`self`\|`match`), `body`, optional `sent_at`. Only `self` messages are scored. |
| `preferences` | string | Optional, ≤2000 chars. What the customer is looking for; feedback is tailored to it. |

Response:

| Field | Type | Notes |
| --- | --- | --- |
| `model_version` | string | Which backend/model produced the scores. Stored with the result. |
| `segments[]` | array | `start_position`, `end_position`, `engagement_score` (0–1), `label` (`engaging`\|`neutral`\|`flat`), `comment`. |
| `overall` | object | `engagement_score`, `summary`, `strengths[]`, `improvements[]`, `reflection_questions[]`, `patterns[]`. |

`overall.reflection_questions[]` are open, non-directive questions that turn the
analysis inward ("did you actually enjoy this conversation?"); the heuristic
always emits at least one when the customer wrote anything.
`overall.patterns[]` are short observations naming recurring behaviour across
the whole conversation — e.g. "You sent about twice as many messages as they
did in this conversation (5 to 2)." — and are empty when nothing recurs. Both
default to `[]`, so clients that predate them are unaffected.

`label` is always derived from the score (≥0.66 engaging, ≥0.4 neutral, else
flat), so it is consistent across backends. `comment`, `summary` and
`improvements` describe reply outcomes (no reply / short reply / engaged
reply) and hand them back as something to think about; they never contain a
drafted message (enforced by the LLM parser) and are not meant to state why
the match replied the way they did (a prompt rule for the LLM backend).

### `POST /analyze/images` (photos)

Request:

| Field | Type | Notes |
| --- | --- | --- |
| `images[]` | array | 1–10 entries of `{url?, base64?, media_type?}`. Exactly one of `url` / `base64` per entry. `base64` is the raw payload (no `data:` prefix). `media_type` is one of `image/jpeg` (default), `image/png`, `image/webp`, `image/gif`. |
| `preferences` | string | Optional, ≤2000 chars. Same meaning as on `/analyze`. |

Response:

| Field | Type | Notes |
| --- | --- | --- |
| `model_version` | string | `image-llm-<provider>-<model>` or `image-heuristic-v1`. |
| `images[]` | array | One per request image, by `index`: `clarity_score` (0–1), `is_clear`, `subject_focus_score` (0–1), `is_customer_focal_point`, `feedback`. |
| `overall` | object | `summary`, `strengths[]`, `improvements[]`. |

### `POST /summarize/reviews` (coach reviews)

Request: `{coach_name?, reviews: [{rating: 1-5, comment}]}`. The rating is never
echoed back as a score; a review with `rating >= 4` counts as a recommendation.

Response: `model_version` (`reviews-llm-<provider>-<model>` or
`reviews-heuristic-v1`), `recommended`, `total`, `summary` (starts with the
recommendation line, e.g. "3 of 4 clients recommend Ava.") and `strengths[]`
(0–5 short phrases naming what recommending clients praise; empty when there
are no recommendations or no written comments). The heuristic
matches comments against fixed strength themes; the LLM summarises free text
and falls back to the heuristic on any failure.

The response is synchronous and the service stores nothing.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `ML_BACKEND` | `llm` | `llm`, `trained` or `heuristic`. |
| `LLM_PROVIDER` | `openai` | `openai` (also any OpenAI-compatible gateway: Azure, Ollama, vLLM) or `anthropic`. |
| `LLM_API_KEY` | – | If unset, the service logs a warning and falls back to the heuristic scorer on both tracks. |
| `LLM_MODEL` | `gpt-4o-mini` / `claude-3-5-haiku-latest` | Per provider. |
| `LLM_BASE_URL` | provider default | Point at a self-hosted gateway. |
| `LLM_TIMEOUT_SECONDS` | `45` | |
| `ML_MODEL_DIR` | `artifacts/segment-scorer` | Checkpoint used when `ML_BACKEND=trained`. |
| `ML_SEGMENT_SIZE` | `4` | Messages per segment. |

## Backends

`app/scoring/` contains one class per backend behind the `Scorer` ABC
(`analyze(request) -> AnalyzeResponse`). `build_scorer()` picks one from env.
The image track has its own `ImageScorer` ABC in `image.py`
(`analyze(request) -> ImageAnalyzeResponse`) and `build_image_scorer()`, which
reads the same `LLM_*` variables: `LLMImageScorer` sends the photos to the
vision endpoint of the configured provider (Anthropic or OpenAI-compatible);
`HeuristicImageScorer` is the no-key fallback and is also used when
`ML_BACKEND=heuristic`. There is no trained image backend.

- **`llm.py` (v1, default)** — prompts the LLM with the transcript and the exact
  segment boundaries, parses strict JSON, clamps and re-labels every score. Any
  failure (timeout, bad JSON, rate limit) degrades to the heuristic scorer and
  reports it in `model_version` rather than failing the analysis job.
- **`heuristic.py`** — no network, no model. Length/question/reciprocity signals.
  Used for local dev, CI and as the LLM fallback.
- **`trained.py`** — a fine-tuned checkpoint. torch/transformers are imported
  lazily so the serving image stays small until you actually use it. It
  predates the message track's rules: it scores every segment regardless of
  sender and ignores `preferences`, so the self-only / no-drafting guarantees
  above hold for `llm` and `heuristic` only.

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

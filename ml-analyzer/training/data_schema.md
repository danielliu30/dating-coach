# Labeled training example schema

One JSON object per line (JSONL). This is the only format `training/dataset.py`
reads, and it mirrors the `training_examples` table in the Go backend.

```json
{
  "conversation_id": "9f2b...",
  "platform": "hinge",
  "consented": true,
  "reply_received": true,
  "outcome": "number_exchanged",
  "messages": [
    {"position": 0, "sender": "self",  "body": "your profile says you bake — what's the last thing you made?", "sent_at": "2026-03-01T18:02:11Z"},
    {"position": 1, "sender": "match", "body": "sourdough, badly. it came out like a frisbee", "sent_at": "2026-03-01T18:20:04Z"}
  ],
  "segments": [
    {"start_position": 0, "end_position": 3, "engagement_score": 0.82}
  ]
}
```

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `conversation_id` | string | yes | Stable id; used for de-duplication and user-disjoint splits. |
| `platform` | string | no | `hinge`, `tinder`, `bumble`, `unknown`. Useful as a slice for evaluation. |
| `consented` | bool | yes | `dataset.load_jsonl` drops rows without it unless `--include-unconsented`. |
| `messages[].position` | int | yes | 0-based, contiguous, ordered. |
| `messages[].sender` | enum | yes | `self` (the coached user) or `match`. |
| `messages[].body` | string | yes | Pseudonymised text (see README on PII). |
| `messages[].sent_at` | RFC3339 | no | Enables response-latency features later. |
| `segments` | array | no | Human per-segment labels. When present they are the training target. |
| `segments[].engagement_score` | float 0–1 | yes if segment given | 1 = clearly hooked, 0 = dead thread. |
| `reply_received` | bool | no | Weak label: did the match reply to the last `self` message? |
| `outcome` | enum | no | `ghosted`, `kept_talking`, `number_exchanged`, `date_set`. |

## Label precedence

1. `segments[].engagement_score` — human labels, used directly.
2. `outcome` — mapped to a conversation-level weak score
   (`ghosted` 0.05, `kept_talking` 0.55, `number_exchanged` 0.8, `date_set` 0.95)
   and applied to the whole conversation.
3. `reply_received` — coarsest fallback (0.6 / 0.2).

Rows with none of the three are skipped.

## Annotation guidance

Rate a segment on how easy and appealing it is to reply to, not on writing
quality:

| Score | Looks like |
| --- | --- |
| 0.8–1.0 | Specific, curious, builds on what the other person said; mutual initiative. |
| 0.5–0.7 | Friendly and alive, but surface-level; questions are generic. |
| 0.2–0.4 | One-sided; short reactions, no hooks offered. |
| 0.0–0.2 | Dead: repeated one-word replies, ignored questions, long unexplained gaps. |

Two annotators per conversation; adjudicate any disagreement above 0.25.

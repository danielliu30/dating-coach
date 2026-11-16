import json

from training.dataset import TrainingExample, load_jsonl, to_segment_rows


def _row(**overrides):
    row = {
        "conversation_id": "c1",
        "consented": True,
        "outcome": "ghosted",
        "messages": [
            {"position": 0, "sender": "self", "body": "hey"},
            {"position": 1, "sender": "match", "body": "hi"},
        ],
    }
    row.update(overrides)
    return row


def test_null_segments_fall_back_to_the_outcome_weak_label(tmp_path):
    # App-sourced training_examples rows stored segment_labels as jsonb null.
    path = tmp_path / "labeled.jsonl"
    path.write_text(json.dumps(_row(segments=None)) + "\n", encoding="utf-8")

    examples = load_jsonl(path)

    assert examples[0].segments == []
    assert list(to_segment_rows(examples)) == [("[0] self: hey\n[1] match: hi", 0.05, 1.0)]


def test_human_segments_still_win_over_the_outcome():
    example = TrainingExample.from_json(
        _row(segments=[{"start_position": 0, "end_position": 1, "engagement_score": 0.7}])
    )

    assert list(to_segment_rows([example])) == [("[0] self: hey\n[1] match: hi", 0.7, 1.0)]


def _example(messages: list[dict], **extra: object) -> TrainingExample:
    return TrainingExample.from_json({"conversation_id": "c1", "consented": True, "messages": messages, **extra})


def test_segment_rows_carry_meta_line_from_sent_at() -> None:
    """Human-labeled windows get the [meta] features computed over that window only."""
    example = _example(
        [
            {"position": 0, "sender": "self", "body": "what did you bake?", "sent_at": "2026-03-01T18:00:00Z"},
            {"position": 1, "sender": "match", "body": "sourdough", "sent_at": "2026-03-01T18:12:00Z"},
            {"position": 2, "sender": "self", "body": "ha", "sent_at": "2026-03-01T18:15:00Z"},
            {"position": 3, "sender": "match", "body": "you?", "sent_at": "2026-03-01T19:15:00Z"},
        ],
        segments=[{"start_position": 0, "end_position": 2, "engagement_score": 0.7}],
    )
    assert list(to_segment_rows([example])) == [
        (
            "[meta] match_latency_min=12 self_latency_min=3 self_msgs=2 match_msgs=1\n"
            "[0] self: what did you bake?\n[1] match: sourdough\n[2] self: ha",
            0.7,
            1.0,
        )
    ]


def test_rows_without_sent_at_train_on_plain_transcript() -> None:
    """Old exports with no timestamps still produce rows, just without the [meta] line."""
    example = _example(
        [{"position": 0, "sender": "self", "body": "hey"}, {"position": 1, "sender": "match", "body": "hi"}],
        outcome="kept_talking",
    )
    assert list(to_segment_rows([example])) == [("[0] self: hey\n[1] match: hi", 0.55, 1.0)]

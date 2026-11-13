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
    assert list(to_segment_rows(examples)) == [("[0] self: hey\n[1] match: hi", 0.05)]


def test_human_segments_still_win_over_the_outcome():
    example = TrainingExample.from_json(
        _row(segments=[{"start_position": 0, "end_position": 1, "engagement_score": 0.7}])
    )

    assert list(to_segment_rows([example])) == [("[0] self: hey\n[1] match: hi", 0.7)]

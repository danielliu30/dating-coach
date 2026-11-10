import json
from pathlib import Path

import train
from training.agreement import label_counts
from training.dataset import TrainingExample, exclude_holdout, load_jsonl, to_segment_rows

HOLDOUT = Path(__file__).resolve().parent.parent / "data" / "eval_holdout.jsonl"


def _example(conversation_id: str, user_id: str | None = None) -> TrainingExample:
    """Build a minimal consented example with one human-labeled segment."""
    return TrainingExample.from_json(
        {
            "conversation_id": conversation_id,
            "user_id": user_id,
            "consented": True,
            "messages": [{"position": 0, "sender": "self", "body": "hi"}],
            "segments": [{"start_position": 0, "end_position": 0, "engagement_score": 0.5}],
        }
    )


def test_exclude_holdout_drops_shared_conversations_and_users() -> None:
    """Rows of a holdout conversation, or of any holdout user, never reach training."""
    holdout = [_example("h1", "u1"), _example("h2")]
    examples = [
        _example("h1", "u1"),  # the holdout row itself
        _example("c2", "u1"),  # another conversation of a holdout user
        _example("h2", "u9"),  # holdout conversation id re-exported under a user
        _example("c3", "u2"),
        _example("c4"),
    ]
    kept, dropped = exclude_holdout(examples, holdout)
    assert [e.conversation_id for e in kept] == ["c3", "c4"]
    assert dropped == 3


def test_exclude_holdout_ignores_missing_user_ids() -> None:
    """Examples without user_id are not matched against holdout examples without user_id."""
    kept, dropped = exclude_holdout([_example("c1"), _example("c2")], [_example("h1")])
    assert [e.conversation_id for e in kept] == ["c1", "c2"]
    assert dropped == 0


def test_frozen_holdout_is_valid_and_matches_served_segments() -> None:
    """The committed holdout loads, is consented, user-disjoint, and labels every served segment."""
    examples = load_jsonl(HOLDOUT)
    assert len(examples) == sum(1 for line in HOLDOUT.read_text(encoding="utf-8").splitlines() if line.strip())
    assert len({e.group_key() for e in examples}) == len(examples)
    for example in examples:
        positions = [m.position for m in example.messages]
        assert positions == list(range(len(positions)))
        # Human labels sit on the default ML_SEGMENT_SIZE=4 boundaries that both scorers emit.
        expected = [(s, min(s + 3, len(positions) - 1)) for s in range(0, len(positions), 4)]
        assert [(s.start_position, s.end_position) for s in example.segments] == expected
    scores = [score for _, score in to_segment_rows(examples)]
    assert all(label_counts(scores).values()), "holdout must cover every served label bucket"


def test_report_holdout_prints_stats_and_excludes_overlap(tmp_path: Path, capsys) -> None:
    """train.py reports holdout label stats and removes overlapping rows before the split."""
    holdout = tmp_path / "holdout.jsonl"
    rows = [
        {"conversation_id": "h1", "user_id": "u1", "consented": True,
         "messages": [{"position": 0, "sender": "self", "body": "hi"}],
         "segments": [{"start_position": 0, "end_position": 0, "engagement_score": 0.2}]},
        {"conversation_id": "h2", "user_id": "u2", "consented": True,
         "messages": [{"position": 0, "sender": "self", "body": "hey"}],
         "segments": [{"start_position": 0, "end_position": 0, "engagement_score": 0.8}]},
    ]
    holdout.write_text("\n".join(json.dumps(r) for r in rows), encoding="utf-8")
    kept = train.report_holdout(holdout, [_example("h1", "u1"), _example("c1", "u3")])
    assert [e.conversation_id for e in kept] == ["c1"]
    out = capsys.readouterr().out
    assert "conversations=2 segments=2 flat=1 neutral=0 engaging=1" in out
    assert "label_mean=0.500 label_std=0.300 excluded_from_training=1" in out


def test_report_holdout_without_file_keeps_everything(tmp_path: Path, capsys) -> None:
    """A missing holdout file is reported and excludes nothing."""
    examples = [_example("c1")]
    assert train.report_holdout(tmp_path / "missing.jsonl", examples) == examples
    assert "no frozen eval file" in capsys.readouterr().out

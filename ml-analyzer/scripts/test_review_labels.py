import json

import pytest

from scripts import review_labels
from scripts.review_labels import Annotations, adjudication_rows, disagrees, final_rows, report
from training.dataset import load_jsonl, to_segment_rows

MESSAGES = [
    {"position": i, "sender": "self" if i % 2 == 0 else "match", "body": f"message {i}", "sent_at": None}
    for i in range(4)
]


def _row(cid, scores, suggested=(0.8, 0.4), **extra):
    """A pre-label row an annotator filled in; ``scores`` per segment (None = skipped)."""
    bounds = [(0, 1), (2, 3)][: len(scores)]
    return {
        "conversation_id": cid,
        "user_id": "u1",
        "platform": "hinge",
        "consented": True,
        "outcome": "kept_talking",
        "reply_received": True,
        # Distinct bodies per conversation, so load_jsonl dedup keeps each one.
        "messages": [{**m, "body": f"{cid} {m['body']}"} for m in MESSAGES],
        "label_source": "annotator",
        "prelabel": {"model_version": "llm-test", "fallback": False},
        "segments": [
            {"start_position": s, "end_position": e, "suggested_score": sug, "suggested_comment": "", "engagement_score": sc}
            for (s, e), sug, sc in zip(bounds, suggested, scores)
        ],
        **extra,
    }


def _write(path, rows):
    path.write_text("".join(json.dumps(r) + "\n" for r in rows))
    return path


@pytest.fixture
def files(tmp_path):
    alice = _write(tmp_path / "alice.jsonl", [_row("c1", [0.8, 0.2]), _row("c2", [0.5])])
    bob = _write(tmp_path / "bob.jsonl", [_row("c1", [0.7, 0.6]), _row("c2", [None])])
    return alice, bob


def _annotations(*paths):
    ann = Annotations()
    for path in paths:
        ann.add_file(path)
    return ann


def test_threshold_is_strictly_above_a_quarter():
    assert not disagrees(0.6, 0.85)
    assert disagrees(0.6, 0.86)


def test_report_flags_disagreement(files):
    assert report(_annotations(*files)) == [
        "segments: 3",
        "needs a second annotator: 1 segment(s)",
        "adjudicate (spread > 0.25): 1 segment(s)",
    ]


def test_adjudication_rows_carry_both_scores(files):
    [row] = adjudication_rows(_annotations(*files))
    assert row["conversation_id"] == "c1" and row["annotator"] == "adjudicator"
    assert row["segments"] == [
        {"start_position": 2, "end_position": 3,
         "annotator_scores": {"alice": 0.2, "bob": 0.6}, "engagement_score": None}
    ]


def test_final_rows_use_agreeing_mean_and_adjudicated_scores(files):
    ann = _annotations(*files)
    [row] = final_rows(ann, {})
    assert row["conversation_id"] == "c1" and row["label_source"] == "annotator"
    assert row["segments"] == [{"start_position": 0, "end_position": 1, "engagement_score": 0.75}]
    [row] = final_rows(ann, {("c1", 2, 3): 0.4})
    assert [s["engagement_score"] for s in row["segments"]] == [0.75, 0.4]


def test_annotator_field_overrides_file_name(tmp_path):
    path = _write(tmp_path / "copy.jsonl", [_row("c1", [0.5], annotator="carol")])
    assert _annotations(path).scores[("c1", 0, 1)] == {"carol": 0.5}


@pytest.mark.parametrize("score", [1.5, -0.1, float("nan"), float("inf")])
def test_out_of_range_score_is_rejected(tmp_path, score):
    path = _write(tmp_path / "a.jsonl", [_row("c1", [score])])
    with pytest.raises(SystemExit, match="outside 0-1"):
        _annotations(path)
    with pytest.raises(SystemExit, match="outside 0-1"):
        review_labels.read_adjudicated(path)


def test_main_round_trip_produces_a_trainable_dataset(files, tmp_path):
    base = _write(
        tmp_path / "labeled.jsonl",
        [
            {**_row("c1", []), "label_source": "user", "segments": []},
            {**_row("c1", []), "label_source": "coach", "segments": []},
            {**_row("c3", []), "label_source": "user", "segments": []},
        ],
    )
    adjudicate, final = tmp_path / "adjudicate.jsonl", tmp_path / "train.jsonl"
    review_labels.main([*map(str, files), "--adjudication-out", str(adjudicate)])
    rows = [json.loads(line) for line in adjudicate.read_text().splitlines()]
    rows[0]["segments"][0]["engagement_score"] = 0.45
    _write(adjudicate, rows)

    review_labels.main(
        [*map(str, files), "--adjudicated", str(adjudicate), "--final-out", str(final), "--base", str(base)]
    )
    examples = load_jsonl(final)
    assert sorted(e.conversation_id for e in examples) == ["c1", "c3"]
    c1 = next(e for e in examples if e.conversation_id == "c1")
    assert [s.engagement_score for s in c1.segments] == [0.75, 0.45]
    assert [(score, weight) for _, score, weight in to_segment_rows([c1])] == [(0.75, 1.0), (0.45, 1.0)]


@pytest.mark.parametrize("opted_out", [False, True])
def test_base_merge_drops_missing_or_unconsented_conversations(files, tmp_path, opted_out):
    rows = [_row("c3", [], label_source="user"), _row("c4", [], consented=False)]
    if opted_out:
        rows.append(_row("c1", [], consented=False))
    base = _write(tmp_path / "labeled.jsonl", rows)
    final = tmp_path / "train.jsonl"
    review_labels.main([*map(str, files), "--final-out", str(final), "--base", str(base)])
    assert [e.conversation_id for e in load_jsonl(final)] == ["c3"]
    assert [r["conversation_id"] for r in review_labels.jsonl_rows(final)] == ["c3"]


@pytest.mark.parametrize("bob_score", [None, 0.9])
def test_unresolved_annotations_do_not_retain_base_weak_labels(tmp_path, bob_score):
    alice = _write(tmp_path / "alice.jsonl", [_row("c1", [0.1])])
    bob = _write(tmp_path / "bob.jsonl", [_row("c1", [bob_score])])
    base = _write(tmp_path / "base.jsonl", [_row("c1", [], label_source="user")])
    final = tmp_path / "train.jsonl"
    review_labels.main([str(alice), str(bob), "--base", str(base), "--final-out", str(final)])
    assert final.read_text() == ""


def test_adjudication_out_cannot_overwrite_the_adjudicated_file(files, tmp_path):
    adjudicated = _write(tmp_path / "adjudicate.jsonl", [])
    with pytest.raises(SystemExit):
        review_labels.main([*map(str, files), "--adjudication-out", str(adjudicated), "--adjudicated", str(adjudicated)])


def test_unscored_segment_without_a_suggestion_still_needs_annotators(tmp_path):
    path = _write(tmp_path / "a.jsonl", [_row("c1", [None], suggested=(None,))])
    text = "\n".join(report(_annotations(path)))
    assert "segments: 1" in text and "needs a second annotator: 1 segment(s)" in text


def test_same_named_files_without_annotator_field_are_rejected(tmp_path):
    (tmp_path / "alice").mkdir()
    (tmp_path / "bob").mkdir()
    a = _write(tmp_path / "alice" / "labels.jsonl", [_row("c1", [0.2])])
    b = _write(tmp_path / "bob" / "labels.jsonl", [_row("c1", [0.9])])
    with pytest.raises(SystemExit, match="set an annotator field"):
        _annotations(a, b)

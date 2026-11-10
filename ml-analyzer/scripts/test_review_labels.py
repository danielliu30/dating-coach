import json

import pytest

from scripts import review_labels
from scripts.review_labels import Annotations, adjudication_rows, disagrees, final_rows, krippendorff_alpha, report
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
        "messages": MESSAGES,
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


def test_krippendorff_alpha_interval():
    assert krippendorff_alpha([[0.2, 0.4], [0.8, 0.6]]) == pytest.approx(0.7)
    assert krippendorff_alpha([[0.3, 0.3], [0.9, 0.9], [0.5]]) == pytest.approx(1.0)
    assert krippendorff_alpha([[0.5]]) is None


def test_report_diffs_suggestions_and_flags_disagreement(files):
    text = "\n".join(report(_annotations(*files)))
    assert "alice: n=3 mae=0.167 mean_shift=-0.167 within_0.25=67% unchanged=33%" in text
    assert "bob: n=2 mae=0.150 mean_shift=+0.050 within_0.25=100% unchanged=0%" in text
    assert "alice vs bob: n=2 mae=0.250 within_0.25=50% pearson_r=1.000" in text
    assert "needs a second annotator: 1 segment(s)" in text
    assert "adjudicate (spread > 0.25): 1 segment(s)\n  c1 [2-3]: alice=0.20, bob=0.60" in text


def test_adjudication_rows_carry_both_scores(files):
    [row] = adjudication_rows(_annotations(*files))
    assert row["conversation_id"] == "c1" and row["annotator"] == "adjudicator"
    assert row["segments"] == [
        {"start_position": 2, "end_position": 3, "suggested_score": 0.4,
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
    assert _annotations(path).annotators == ["carol"]


def test_out_of_range_score_is_rejected(tmp_path):
    path = _write(tmp_path / "a.jsonl", [_row("c1", [1.5])])
    with pytest.raises(SystemExit, match="outside 0-1"):
        _annotations(path)


def test_main_round_trip_produces_a_trainable_dataset(files, tmp_path):
    base = _write(
        tmp_path / "labeled.jsonl",
        [
            {**_row("c1", []), "label_source": "user", "segments": []},
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
    assert [score for _, score in to_segment_rows([c1])] == [0.75, 0.45]


def test_base_merge_drops_conversations_no_longer_in_the_export(files, tmp_path):
    base = _write(tmp_path / "labeled.jsonl", [{**_row("c3", []), "label_source": "user", "segments": []}])
    final = tmp_path / "train.jsonl"
    review_labels.main([*map(str, files), "--final-out", str(final), "--base", str(base)])
    assert [e.conversation_id for e in load_jsonl(final)] == ["c3"]

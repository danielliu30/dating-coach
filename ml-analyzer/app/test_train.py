import json
import math
from pathlib import Path

import pytest

import train


def test_prediction_metrics_expose_a_squashed_head() -> None:
    """Predictions squashed into a narrow band show up as pred_std far below label_std."""
    labels = [0.05, 0.2, 0.5, 0.8, 0.95]
    squashed = [0.5 + 0.23 * y for y in labels]  # the [0.5, 0.73] band plain MSE on logits produces
    result = train.prediction_metrics(squashed, labels)
    assert result["pred_min"] >= 0.5 and result["pred_max"] <= 0.73
    assert result["pred_std"] == pytest.approx(0.23 * result["label_std"])
    assert result["label_mean"] == pytest.approx(0.5)
    assert result["mae"] == pytest.approx(sum(abs(p - y) for p, y in zip(squashed, labels)) / 5)


def test_prediction_metrics_rmse_and_perfect_fit() -> None:
    """RMSE is the root of the mean squared error and zero for a perfect fit."""
    result = train.prediction_metrics([0.2, 0.6], [0.4, 0.6])
    assert result["rmse"] == pytest.approx(math.sqrt(0.02))
    assert result["mae"] == pytest.approx(0.1)
    perfect = train.prediction_metrics([0.1, 0.9], [0.1, 0.9])
    assert perfect["mae"] == 0 and perfect["rmse"] == 0
    assert perfect["pred_std"] == perfect["label_std"]


def test_prediction_metrics_empty_is_nan() -> None:
    """An empty eval split yields nan metrics rather than dividing by zero."""
    assert all(math.isnan(v) for v in train.prediction_metrics([], []).values())


def test_write_eval_predictions_writes_one_line_per_segment(tmp_path: Path) -> None:
    """Each eval segment becomes a JSONL record with its label, prediction and label_for buckets."""
    path = tmp_path / "segment-scorer" / "eval_predictions.jsonl"
    rows = [("[0] self: hey", 0.1), ("[0] self: what was the last thing you baked?", 0.8)]
    train.write_eval_predictions(path, rows, [0.55, 0.7])
    records = [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]
    assert records == [
        {
            "text": "[0] self: hey",
            "label": 0.1,
            "prediction": 0.55,
            "label_bucket": "flat",
            "prediction_bucket": "neutral",
        },
        {
            "text": "[0] self: what was the last thing you baked?",
            "label": 0.8,
            "prediction": 0.7,
            "label_bucket": "engaging",
            "prediction_bucket": "engaging",
        },
    ]


def test_write_eval_predictions_rejects_misaligned_predictions(tmp_path: Path) -> None:
    """A prediction count that does not match the eval rows is a bug, not a silent truncation."""
    with pytest.raises(ValueError, match="2 eval rows but 1 predictions"):
        train.write_eval_predictions(tmp_path / "p.jsonl", [("a", 0.1), ("b", 0.2)], [0.5])

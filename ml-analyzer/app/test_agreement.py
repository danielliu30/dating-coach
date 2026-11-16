import math

import pytest

from training.agreement import (
    agreement,
    cohen_kappa,
    distribution,
    label_counts,
    label_kappa,
    mae,
    pearson,
    spearman,
)


def test_mae_is_mean_absolute_difference() -> None:
    """MAE averages absolute per-segment differences, not signed ones."""
    assert mae([0.2, 0.8, 0.5], [0.3, 0.6, 0.5]) == pytest.approx(0.1)
    assert mae([0.1, 0.9], [0.9, 0.1]) == pytest.approx(0.8)


def test_identical_vectors_agree_perfectly() -> None:
    """A scorer compared with itself has zero MAE and perfect correlation and kappa."""
    scores = [0.1, 0.35, 0.5, 0.62, 0.7, 0.95]
    assert agreement(scores, scores) == {
        "segments": 6,
        "mae": 0.0,
        "pearson": pytest.approx(1.0),
        "spearman": pytest.approx(1.0),
        "kappa": pytest.approx(1.0),
    }


def test_pearson_is_scale_and_shift_invariant() -> None:
    """A linear rescaling (e.g. a sigmoid-squashed but linear range) keeps pearson at 1."""
    a = [0.1, 0.3, 0.5, 0.9]
    assert pearson(a, [0.5 + x / 4 for x in a]) == pytest.approx(1.0)
    assert pearson(a, [1 - x for x in a]) == pytest.approx(-1.0)


def test_spearman_rewards_monotone_nonlinear_agreement() -> None:
    """Spearman is 1 for any monotone transform, while pearson drops below 1."""
    a = [0.1, 0.2, 0.3, 0.4, 0.95]
    b = [x**4 for x in a]
    assert spearman(a, b) == pytest.approx(1.0)
    assert pearson(a, b) < 0.99


def test_spearman_averages_tied_ranks() -> None:
    """Ties share their average rank, matching scipy.stats.spearmanr."""
    # scipy.stats.spearmanr([1, 2, 2, 3], [1, 3, 2, 4]).statistic == 0.9486832980505138
    assert spearman([1, 2, 2, 3], [1, 3, 2, 4]) == pytest.approx(0.9486832980505138)


def test_correlation_is_nan_for_constant_vectors() -> None:
    """A collapsed predictor (all scores equal) has undefined correlation, never a passing one."""
    assert math.isnan(pearson([0.6, 0.6, 0.6], [0.1, 0.5, 0.9]))
    assert math.isnan(spearman([0.6, 0.6, 0.6], [0.1, 0.5, 0.9]))
    assert math.isnan(pearson([0.5], [0.5]))
    # 0.1 * 3 / 3 != 0.1 in floats; the rounding must not fake variance.
    assert math.isnan(pearson([0.1, 0.1, 0.1], [0.1, 0.1, 0.1]))
    assert math.isnan(pearson([0.1, 0.1, 0.1], [0.2, 0.5, 0.9]))
    assert math.isnan(pearson([0.0, 1e-200], [0.0, 1.0]))


def test_cohen_kappa_matches_textbook_example() -> None:
    """Two raters, 50 items: observed 0.7, chance 0.5 -> kappa 0.4 (Wikipedia worked example)."""
    a = ["yes"] * 20 + ["yes"] * 5 + ["no"] * 10 + ["no"] * 15
    b = ["yes"] * 20 + ["no"] * 5 + ["yes"] * 10 + ["no"] * 15
    assert cohen_kappa(a, b) == pytest.approx(0.4)


def test_cohen_kappa_is_zero_at_chance_and_negative_below() -> None:
    """Agreement no better than chance is 0; systematic disagreement is negative."""
    assert cohen_kappa(["a", "a", "b", "b"], ["a", "b", "a", "b"]) == pytest.approx(0.0)
    assert cohen_kappa(["a", "b", "a", "b"], ["b", "a", "b", "a"]) == pytest.approx(-1.0)


def test_cohen_kappa_is_nan_when_only_one_category_is_used() -> None:
    """Kappa is undefined when both raters used a single shared category."""
    assert math.isnan(cohen_kappa(["neutral"] * 4, ["neutral"] * 4))


def test_label_kappa_buckets_with_served_thresholds() -> None:
    """Scores are bucketed with label_for (>=0.66 engaging, >=0.4 neutral, else flat) before kappa."""
    # Different scores, same buckets -> perfect agreement on labels.
    assert label_kappa([0.1, 0.45, 0.9], [0.39, 0.65, 0.66]) == pytest.approx(1.0)
    # Small MAE but every score straddles a boundary -> no label agreement.
    a = [0.39, 0.65, 0.39, 0.65]
    b = [0.40, 0.66, 0.40, 0.66]
    assert mae(a, b) == pytest.approx(0.01)
    assert label_kappa(a, b) < 0


def test_sigmoid_squash_is_visible_in_distribution_and_kappa() -> None:
    """Predictions squashed into [0.5, 0.73] correlate perfectly yet collapse spread and label agreement."""
    labels = [0.05, 0.2, 0.35, 0.5, 0.65, 0.8, 0.95]
    squashed = [1 / (1 + math.exp(-x)) for x in labels]
    assert pearson(labels, squashed) > 0.99
    assert distribution(squashed)["std"] < distribution(labels)["std"] / 3
    assert label_counts(squashed)["flat"] == 0
    assert label_kappa(labels, squashed) == pytest.approx(0.4)


def test_distribution_reports_population_stats() -> None:
    """Distribution returns mean, population std, min and max."""
    assert distribution([0.2, 0.4, 0.6, 0.8]) == pytest.approx(
        {"mean": 0.5, "std": math.sqrt(0.05), "min": 0.2, "max": 0.8}
    )
    assert all(math.isnan(v) for v in distribution([]).values())


def test_label_counts_cover_every_bucket() -> None:
    """label_counts always reports all three buckets, including empty ones."""
    assert label_counts([0.1, 0.5, 0.7, 0.8]) == {"flat": 1, "neutral": 1, "engaging": 2}
    assert label_counts([]) == {"flat": 0, "neutral": 0, "engaging": 0}


def test_mismatched_lengths_are_rejected() -> None:
    """Unpaired vectors are a caller bug and raise instead of silently truncating."""
    with pytest.raises(ValueError, match="differ in length"):
        mae([0.1, 0.2], [0.1])
    with pytest.raises(ValueError, match="differ in length"):
        agreement([0.1], [])


def test_empty_input_is_nan() -> None:
    """An empty eval yields nan metrics, so it can never satisfy a serving threshold."""
    result = agreement([], [])
    assert result["segments"] == 0
    assert all(math.isnan(result[k]) for k in ("mae", "pearson", "spearman", "kappa"))

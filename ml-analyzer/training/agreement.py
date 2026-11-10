"""Agreement metrics between two engagement scorers (or a scorer and labels).

Used by ``scripts/eval_vs_llm.py`` to decide whether the trained backend agrees
closely enough with the LLM backend to serve, and by ``train.py`` to show the
score distribution of predictions next to the labels.

Pure Python on purpose: the serving image and CI install only
``requirements.txt``, so these stay importable and testable without numpy.
Undefined results (zero variance, empty input) are ``nan`` rather than an
exception, so a degenerate eval can never pass a ``>`` / ``<`` threshold check.
"""

from __future__ import annotations

import math
from collections import Counter
from typing import Dict, List, Sequence

from app.scoring.base import label_for

LABELS = ("flat", "neutral", "engaging")


def _paired(a: Sequence[float], b: Sequence[float]) -> int:
    if len(a) != len(b):
        raise ValueError(f"score vectors differ in length: {len(a)} != {len(b)}")
    return len(a)


def mae(a: Sequence[float], b: Sequence[float]) -> float:
    """Mean absolute difference between paired scores."""
    n = _paired(a, b)
    if n == 0:
        return math.nan
    return sum(abs(x - y) for x, y in zip(a, b)) / n


def pearson(a: Sequence[float], b: Sequence[float]) -> float:
    """Pearson correlation; ``nan`` when either vector is constant."""
    n = _paired(a, b)
    if n < 2:
        return math.nan
    mean_a = sum(a) / n
    mean_b = sum(b) / n
    cov = sum((x - mean_a) * (y - mean_b) for x, y in zip(a, b))
    var_a = sum((x - mean_a) ** 2 for x in a)
    var_b = sum((y - mean_b) ** 2 for y in b)
    if var_a == 0 or var_b == 0:
        return math.nan
    return cov / math.sqrt(var_a * var_b)


def _ranks(values: Sequence[float]) -> List[float]:
    """1-based ranks, ties sharing their average rank."""
    order = sorted(range(len(values)), key=lambda i: values[i])
    ranks = [0.0] * len(values)
    i = 0
    while i < len(order):
        j = i
        while j + 1 < len(order) and values[order[j + 1]] == values[order[i]]:
            j += 1
        average = (i + j) / 2 + 1
        for k in range(i, j + 1):
            ranks[order[k]] = average
        i = j + 1
    return ranks


def spearman(a: Sequence[float], b: Sequence[float]) -> float:
    """Spearman rank correlation (Pearson on tie-averaged ranks)."""
    _paired(a, b)
    return pearson(_ranks(a), _ranks(b))


def cohen_kappa(a: Sequence[str], b: Sequence[str]) -> float:
    """Unweighted Cohen's kappa between two raters' categorical labels.

    ``nan`` when chance agreement is 1 (both raters used one and the same
    category throughout), where kappa is undefined.
    """
    n = _paired(a, b)
    if n == 0:
        return math.nan
    counts_a, counts_b = Counter(a), Counter(b)
    observed = sum(1 for x, y in zip(a, b) if x == y) / n
    expected = sum(counts_a[c] * counts_b[c] for c in counts_a) / (n * n)
    if expected == 1:
        return math.nan
    return (observed - expected) / (1 - expected)


def label_kappa(a: Sequence[float], b: Sequence[float]) -> float:
    """Cohen's kappa on the served ``label_for`` buckets of two score vectors."""
    _paired(a, b)
    return cohen_kappa([label_for(x) for x in a], [label_for(y) for y in b])


def distribution(values: Sequence[float]) -> Dict[str, float]:
    """Mean, population std, min and max of a score vector."""
    if not values:
        return {"mean": math.nan, "std": math.nan, "min": math.nan, "max": math.nan}
    n = len(values)
    mean = sum(values) / n
    return {
        "mean": mean,
        "std": math.sqrt(sum((v - mean) ** 2 for v in values) / n),
        "min": min(values),
        "max": max(values),
    }


def label_counts(values: Sequence[float]) -> Dict[str, int]:
    """How many scores fall in each ``label_for`` bucket."""
    counts = {label: 0 for label in LABELS}
    for value in values:
        counts[label_for(value)] += 1
    return counts


def agreement(a: Sequence[float], b: Sequence[float]) -> Dict[str, float]:
    """Every agreement metric between two paired score vectors."""
    return {
        "segments": _paired(a, b),
        "mae": mae(a, b),
        "pearson": pearson(a, b),
        "spearman": spearman(a, b),
        "kappa": label_kappa(a, b),
    }

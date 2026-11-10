import sys

import pytest

import train
from training.dataset import TrainingExample, split, to_segment_rows

MESSAGES = [
    {"position": 0, "sender": "match", "body": "hey"},
    {"position": 1, "sender": "match", "body": "you there?"},
    {"position": 2, "sender": "self", "body": "sorry, busy day"},
    {"position": 3, "sender": "match", "body": "no worries"},
]


def _example(conversation_id: str, messages: list[dict], **extra: object) -> TrainingExample:
    return TrainingExample.from_json({"conversation_id": conversation_id, "consented": True, "messages": messages, **extra})


def test_self_only_drops_match_only_segment_windows() -> None:
    """A labeled window without a self message is skipped by default and kept with self_only=False."""
    example = _example(
        "c1",
        MESSAGES,
        segments=[
            {"start_position": 0, "end_position": 1, "engagement_score": 0.3},
            {"start_position": 2, "end_position": 3, "engagement_score": 0.8},
        ],
    )
    assert [score for _, score in to_segment_rows([example])] == [0.8]
    assert [score for _, score in to_segment_rows([example], self_only=False)] == [0.3, 0.8]


def test_self_only_drops_weak_labeled_conversations_without_self() -> None:
    """The whole-conversation weak-label row needs a self message too."""
    match_only = _example("c2", MESSAGES[:2], outcome="ghosted")
    mixed = _example("c3", MESSAGES, outcome="kept_talking")
    assert [score for _, score in to_segment_rows([match_only, mixed])] == [0.55]
    assert [score for _, score in to_segment_rows([match_only, mixed], self_only=False)] == [0.05, 0.55]


def test_split_applies_self_only_to_both_sides() -> None:
    examples = [_example(f"c{i}", MESSAGES[:2], outcome="ghosted") for i in range(4)]
    assert split(examples, eval_fraction=0.5) == ([], [])
    train_rows, eval_rows = split(examples, eval_fraction=0.5, self_only=False)
    assert (len(train_rows), len(eval_rows)) == (2, 2)


def test_split_does_not_spend_eval_slots_on_filtered_groups() -> None:
    """Match-only groups are dropped before splitting, so usable groups fill both sides.

    ``m1``/``m2`` hash ahead of ``u18``/``u19``, so splitting before filtering would
    hand both eval slots to the match-only groups and leave eval empty.
    """
    match_only = [_example(key, MESSAGES[:2], outcome="ghosted") for key in ("m1", "m2")]
    usable = [_example(key, MESSAGES, outcome="kept_talking") for key in ("u18", "u19")]
    train_rows, eval_rows = split(match_only + usable, eval_fraction=0.5)
    assert (len(train_rows), len(eval_rows)) == (1, 1)


@pytest.mark.parametrize(("flag", "expected"), [([], True), (["--self-only"], True), (["--no-self-only"], False)])
def test_train_self_only_flag_defaults_on(monkeypatch: pytest.MonkeyPatch, flag: list[str], expected: bool) -> None:
    monkeypatch.setattr(sys, "argv", ["train.py", "--data", "x.jsonl", *flag])
    assert train.parse_args().self_only is expected

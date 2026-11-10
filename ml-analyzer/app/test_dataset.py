from training.dataset import TrainingExample, exclude_holdout


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

import json

import training.dataset as dataset_module
from training.dataset import (
    TrainingExample,
    _jaccard,
    _normalized_transcript,
    _shingles,
    deduplicate,
    load_jsonl,
    normalize_body,
)


def _payload(conversation_id, bodies, user_id=None, segments=None, outcome="kept_talking"):
    """Build one JSONL row with alternating self/match messages."""
    return {
        "conversation_id": conversation_id,
        "user_id": user_id,
        "consented": True,
        "outcome": outcome,
        "messages": [
            {"position": i, "sender": "self" if i % 2 == 0 else "match", "body": body}
            for i, body in enumerate(bodies)
        ],
        "segments": segments or [],
    }


def _write(tmp_path, rows):
    path = tmp_path / "labeled.jsonl"
    path.write_text("\n".join(json.dumps(r) for r in rows) + "\n", encoding="utf-8")
    return path


BODIES = [
    "your profile says you bake, what's the last thing you made?",
    "sourdough, badly. it came out like a frisbee",
    "a frisbee loaf is still a loaf. what went wrong with the starter?",
    "i think i killed it on day three honestly",
    "rip. want to try again with mine? it has survived two moves",
    "only if you promise to judge my scoring pattern",
]


def test_normalize_body_lowercases_and_strips_punctuation_and_whitespace():
    assert normalize_body("  Sourdough,   BADLY!!\n it's ok ") == "sourdough badly it s ok"


def test_load_jsonl_drops_repeated_transcripts(tmp_path):
    noisy = [b.upper().replace(",", " ,  ") + "!!" for b in BODIES]
    rows = [
        _payload("a", BODIES, user_id="u1"),
        _payload("b", noisy, user_id="u2"),
        _payload("c", BODIES),
        _payload("d", ["completely", "different", "thread"], user_id="u1"),
    ]

    examples = load_jsonl(_write(tmp_path, rows))

    assert [e.conversation_id for e in examples] == ["a", "d"]


def test_load_jsonl_dedupe_can_be_disabled(tmp_path):
    rows = [_payload("a", BODIES), _payload("b", BODIES)]

    examples = load_jsonl(_write(tmp_path, rows), dedupe=False)

    assert [e.conversation_id for e in examples] == ["a", "b"]


def test_near_duplicates_are_dropped_within_the_same_user_only():
    longer = BODIES + ["deal. saturday?"]
    examples = [
        TrainingExample.from_json(_payload("a", BODIES, user_id="u1")),
        TrainingExample.from_json(_payload("b", longer, user_id="u1")),
        TrainingExample.from_json(_payload("c", BODIES + ["deal. sunday?"], user_id="u2")),
    ]

    kept = deduplicate(examples)

    assert [e.conversation_id for e in kept] == ["a", "c"]


def test_dissimilar_conversations_of_one_user_are_kept():
    examples = [
        TrainingExample.from_json(_payload("a", BODIES, user_id="u1")),
        TrainingExample.from_json(_payload("b", BODIES[:3] + ["anyway gotta go", "ok bye"], user_id="u1")),
    ]

    assert [e.conversation_id for e in deduplicate(examples)] == ["a", "b"]


def test_human_labeled_duplicate_replaces_weak_labeled_copy():
    segments = [{"start_position": 0, "end_position": 3, "engagement_score": 0.82}]
    examples = [
        TrainingExample.from_json(_payload("weak", BODIES, user_id="u1")),
        TrainingExample.from_json(_payload("other", ["hey", "hi"], user_id="u2")),
        TrainingExample.from_json(_payload("human", BODIES, user_id="u3", segments=segments)),
    ]

    kept = deduplicate(examples)

    assert [e.conversation_id for e in kept] == ["human", "other"]
    assert kept[0].segments[0].engagement_score == 0.82


def test_near_copy_of_a_cross_user_exact_copy_is_dropped():
    examples = [
        TrainingExample.from_json(_payload("a", BODIES, user_id="u1")),
        TrainingExample.from_json(_payload("b", BODIES, user_id="u2")),
        TrainingExample.from_json(_payload("c", BODIES + ["deal. saturday?"], user_id="u2")),
    ]

    assert [e.conversation_id for e in deduplicate(examples)] == ["a"]


def test_near_copy_of_a_replaced_representative_stays_in_its_cluster():
    segments = [{"start_position": 0, "end_position": 3, "engagement_score": 0.82}]
    examples = [
        TrainingExample.from_json(_payload("weak", BODIES, user_id="u1")),
        TrainingExample.from_json(
            _payload("human", BODIES + ["deal. saturday?"], user_id="u1", segments=segments)
        ),
        TrainingExample.from_json(_payload("again", BODIES, user_id="u1")),
        TrainingExample.from_json(_payload("other", ["hey", "hi"], user_id="u1")),
    ]

    assert [e.conversation_id for e in deduplicate(examples)] == ["human", "other"]


def test_copy_bridging_two_clusters_merges_them_and_keeps_a_human_label():
    base = [" ".join(f"w{i}x{j}" for j in range(12)) for i in range(9)]
    extra_a = ["alpha beta gamma delta epsilon zeta eta theta"]
    extra_b = ["one two three four five six seven eight"]
    segments = [{"start_position": 0, "end_position": 3, "engagement_score": 0.7}]
    human_a = TrainingExample.from_json(_payload("human_a", base + extra_a, user_id="u1", segments=segments))
    weak_b = TrainingExample.from_json(_payload("weak_b", base + extra_b, user_id="u1"))
    human_c = TrainingExample.from_json(_payload("human_c", base, user_id="u1", segments=segments))

    def similarity(x, y):
        return _jaccard(_shingles(_normalized_transcript(x)), _shingles(_normalized_transcript(y)))

    assert similarity(human_a, weak_b) < 0.9
    assert similarity(human_c, human_a) >= 0.9 and similarity(human_c, weak_b) >= 0.9

    assert [e.conversation_id for e in deduplicate([human_a, weak_b, human_c])] == ["human_a"]
    assert [e.conversation_id for e in deduplicate([weak_b, human_a, human_c])] == ["human_a"]


def test_repeated_copies_of_one_user_skip_the_shingle_scan(monkeypatch):
    calls = []
    real_jaccard = dataset_module._jaccard
    monkeypatch.setattr(dataset_module, "_jaccard", lambda a, b: calls.append(1) or real_jaccard(a, b))
    copies = [TrainingExample.from_json(_payload(f"c{i}", BODIES, user_id="u1")) for i in range(200)]
    other = TrainingExample.from_json(_payload("other", ["hey", "hi"], user_id="u1"))

    kept = deduplicate(copies + [other])

    assert [e.conversation_id for e in kept] == ["c0", "other"]
    assert len(calls) == 1

import json

import pytest

from scripts.export_dataset import Pseudonymiser, main, pseudonymise_row


@pytest.mark.parametrize(
    "text, expected",
    [
        ("mail me at jane.doe+x@gmail.com.", "mail me at [EMAIL]."),
        ("see https://example.com/a?b=1, cool", "see [LINK], cool"),
        ("my site is www.janebakes.co.uk!", "my site is [LINK]!"),
        ("insta.com/jane_doe or linktr.ee/jane", "[LINK] or [LINK]"),
        ("follow @jane_doe.", "follow [HANDLE]."),
        ("my insta is jane_doe92 :)", "my insta is [HANDLE] :)"),
        ("IG: jane.doe", "IG: [HANDLE]"),
        ("text me 415-555-0134 or +44 7700 900123", "text me [PHONE] or [PHONE]"),
        ("met on 2026-03-01 at 18:02, 3 times", "met on 2026-03-01 at 18:02, 3 times"),
    ],
)
def test_contact_details_are_replaced(text, expected):
    assert Pseudonymiser()(text) == expected


def test_known_names_are_replaced_as_whole_words_case_insensitively():
    pseudonymise = Pseudonymiser({"Jane Doe": "[MATCH]", "Sam": "[SELF]"})

    assert pseudonymise("Hey jane! I'm Sam, Jane Doe's friend. Samantha?") == (
        "Hey [MATCH]! I'm [SELF], [MATCH]'s friend. Samantha?"
    )


def test_row_drops_name_columns_and_normalises_null_segments():
    row = {
        "conversation_id": "c1",
        "consented": True,
        "match_name": "Ana",
        "display_name": "Ben Ortiz",
        "segments": None,
        "messages": [
            {"position": 0, "sender": "self", "body": "hi Ana, Ben here"},
            {"position": 1, "sender": "match", "body": "hey ben\nwhat's up"},
        ],
    }

    out = pseudonymise_row(row)

    assert "match_name" not in out and "display_name" not in out
    assert out["segments"] == []
    assert [m["body"] for m in out["messages"]] == ["hi [MATCH], [SELF] here", "hey [SELF]\nwhat's up"]
    assert row["messages"][0]["body"] == "hi Ana, Ben here"


def test_from_raw_writes_only_consented_pseudonymised_rows(tmp_path):
    raw = tmp_path / "raw.jsonl"
    rows = [
        {"conversation_id": "c1", "consented": True, "match_name": "Ana",
         "messages": [{"position": 0, "sender": "self", "body": "Ana, call 415 555 0134"}]},
        {"conversation_id": "c2", "consented": False,
         "messages": [{"position": 0, "sender": "self", "body": "secret"}]},
    ]
    raw.write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")
    out = tmp_path / "data" / "labeled.jsonl"

    main(["--from-raw", str(raw), "--out", str(out)])

    written = [json.loads(line) for line in out.read_text(encoding="utf-8").splitlines()]
    assert [r["conversation_id"] for r in written] == ["c1"]
    assert written[0]["messages"][0]["body"] == "[MATCH], call [PHONE]"


def test_segment_free_text_is_dropped() -> None:
    row = {
        "consented": True,
        "segments": [{"start_position": 0, "end_position": 1, "engagement_score": 0.8, "comment": "Jane gave her number"}],
        "messages": [],
    }
    assert pseudonymise_row(row)["segments"] == [{"start_position": 0, "end_position": 1, "engagement_score": 0.8}]

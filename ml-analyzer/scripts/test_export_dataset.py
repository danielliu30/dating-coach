import json
import sys

import pytest

from scripts.export_dataset import Pseudonymiser, main, psql_rows, pseudonymise_row, write_dataset


@pytest.mark.parametrize(
    "text, expected",
    [
        ("mail me at jane.doe+x@gmail.com.", "mail me at [EMAIL]."),
        ("see https://example.com/a?b=1, cool", "see [LINK], cool"),
        ("my site is www.janebakes.co.uk!", "my site is [LINK]!"),
        ("insta.com/jane_doe or linktr.ee/jane", "[LINK] or [LINK]"),
        ("follow @jane_doe.", "follow [HANDLE]."),
        ("@a IG: b @" + "c" * 40, "[HANDLE] IG: [HANDLE] [HANDLE]"),
        ("my insta is jane_doe92 :)", "my insta is [HANDLE] :)"),
        ("IG: jane.doe", "IG: [HANDLE]"),
        ("text me 415-555-0134 or +44 7700 900123", "text me [PHONE] or [PHONE]"),
        ("met on 2026-03-01 at 18:02, 3 times", "met on 2026-03-01 at 18:02, 3 times"),
        ("sent 2026-03-01 18:02", "sent 2026-03-01 18:02"),
        ("text 415-555-0134: thanks", "text [PHONE]: thanks"),
        ("call me:4155550134", "call me:[PHONE]"),
        ("met 2026-03-01. 5 of us came", "met 2026-03-01. 5 of us came"),
        ("met 2026-03-01 5 of us came", "met 2026-03-01 5 of us came"),
        ("415-555-0134. 5 more", "[PHONE]. 5 more"),
        ("415.555.0134", "[PHONE]"),
        ("415. 555. 0134", "[PHONE]"),
        ("06-12-34-56-78", "[PHONE]"),
        ("on 2026-03-01 4155550134", "on 2026-03-01 [PHONE]"),
    ],
)
def test_contact_details_are_replaced(text, expected):
    assert Pseudonymiser()(text) == expected


def test_known_names_are_replaced_as_whole_words_case_insensitively():
    pseudonymise = Pseudonymiser({"Jane Doe": "[MATCH]", "Sam": "[SELF]"})

    assert pseudonymise("Hey jane! I'm Sam, Jane Doe's friend. Samantha?") == (
        "Hey [MATCH]! I'm [SELF], [MATCH]'s friend. Samantha?"
    )


def test_names_match_across_unicode_case_folding():
    assert Pseudonymiser({"Işıl": "[MATCH]"})("hi ışıl, IŞIL") == "hi [MATCH], [MATCH]"
    assert Pseudonymiser({"Sila": "[MATCH]", "Sıla": "[SELF]"})("Sila, Sıla") == "[MATCH], [SELF]"


def test_segments_with_non_numeric_values_are_dropped():
    segments = [
        {"start_position": 0, "end_position": 1, "engagement_score": 0.5},
        {"start_position": 2, "end_position": 3, "engagement_score": "call jane 4155550134"},
        "free text",
    ]
    out = pseudonymise_row({"consented": True, "messages": [], "segments": segments})
    assert out["segments"] == [{"start_position": 0, "end_position": 1, "engagement_score": 0.5}]


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


def test_export_writes_only_consented_pseudonymised_rows(tmp_path, monkeypatch):
    rows = [
        {"conversation_id": "c1", "consented": True, "match_name": "Ana",
         "messages": [{"position": 0, "sender": "self", "body": "Ana, call 415 555 0134"}]},
        {"conversation_id": "c2", "consented": False,
         "messages": [{"position": 0, "sender": "self", "body": "secret"}]},
    ]
    out = tmp_path / "data" / "labeled.jsonl"
    def query(command, url):
        assert command == ["docker", "exec", "db", "psql"]
        assert url == "postgres://host/db"
        return iter(rows)
    monkeypatch.setattr("scripts.export_dataset.psql_rows", query)

    main(["--psql", "docker exec db psql", "--database-url", "postgres://host/db", "--out", str(out)])

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


def test_psql_rows_preserves_json_escapes():
    row = {"messages": [{"body": "backslash \\ and newline\n"}]}
    command = [sys.executable, "-c", f"print(); print({json.dumps(row)!r})"]
    assert list(psql_rows(command, None)) == [row]


@pytest.mark.parametrize("output", ["import sys; sys.exit(1)", "print('invalid json')"])
def test_failed_export_preserves_existing_dataset(tmp_path, output):
    out = tmp_path / "labeled.jsonl"
    out.write_text("existing\n")
    with pytest.raises((SystemExit, json.JSONDecodeError)):
        write_dataset(psql_rows([sys.executable, "-c", output], None), out)
    assert out.read_text() == "existing\n"


def test_empty_export(tmp_path):
    out = tmp_path / "labeled.jsonl"
    assert write_dataset([], out) == 0
    assert out.read_text() == ""


def test_write_dataset_is_owner_only_and_leaves_no_staging_file(tmp_path):
    out = tmp_path / "labeled.jsonl"
    write_dataset([{"consented": True, "messages": []}], out)
    assert out.stat().st_mode & 0o777 == 0o600
    assert [p.name for p in tmp_path.iterdir()] == ["labeled.jsonl"]

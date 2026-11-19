import json

import httpx
import pytest

from scripts import reddit_fetch, reddit_to_eval
from training.dataset import TEST_ONLY_SOURCES, load_jsonl

CREDS = {"REDDIT_CLIENT_ID": "id", "REDDIT_CLIENT_SECRET": "secret", "REDDIT_USER_AGENT": "dating-coach-test/0.1"}
TRANSCRIPT = """Matched with Jess last week, how did I do?

Me: hey Jess, your hiking pic is great
Jess: thanks! that was mt tam
Me: I love that trail. do you go often?
Jess: most weekends, you?
Me: not enough honestly
my knees hate me
Jess: lol. text me at 415-555-0199 or jess@example.com
Me: will do!
EDIT: she unmatched u/someone"""


def _post(**extra):
    return {"id": "abc", "subreddit": "Tinder", "selftext": TRANSCRIPT, "author_fullname": "t2_x", "is_self": True, **extra}


def test_fetch_refuses_without_credentials(monkeypatch):
    for name in CREDS:
        monkeypatch.delenv(name, raising=False)
    with pytest.raises(reddit_fetch.MissingCredentials, match="REDDIT_CLIENT_ID"):
        reddit_fetch.main(["--subreddit", "Tinder"])


def test_fetch_uses_oauth_pages_and_keeps_safe_self_posts(monkeypatch, tmp_path):
    for name, value in CREDS.items():
        monkeypatch.setenv(name, value)
    seen = []

    def handler(request: httpx.Request) -> httpx.Response:
        seen.append(request)
        if request.url.path == "/api/v1/access_token":
            assert request.headers["authorization"].startswith("Basic ")
            return httpx.Response(200, json={"access_token": "tok"})
        assert request.headers["authorization"] == "bearer tok"
        assert request.url.params["q"] == "conversation"
        page = request.url.params.get("after")
        children = [
            {"data": {**_post(id="p1"), "comments": "never kept"}},
            {"data": _post(id="p2", is_self=False)},
            {"data": _post(id="p3", over_18=True)},
        ] if page is None else [{"data": _post(id="p4")}]
        return httpx.Response(200, json={"data": {"children": children, "after": None if page else "t3_p3"}})

    out = tmp_path / "raw.jsonl"
    client = httpx.Client(transport=httpx.MockTransport(handler))
    assert reddit_fetch.main(["--subreddit", "Tinder", "--query", "conversation", "--out", str(out)], client=client) == 0
    rows = [json.loads(line) for line in out.read_text().splitlines()]
    assert [r["id"] for r in rows] == ["p1", "p4"]
    assert set(rows[0]) == set(reddit_fetch.KEPT_FIELDS)
    assert all("/comments" not in str(r.url) for r in seen)


def test_transcript_post_becomes_a_scrubbed_test_only_row():
    row = reddit_to_eval.to_row(_post(), salt="s")
    assert row["source"] == "reddit" and row["consented"] is False and row["platform"] == "tinder"
    assert row["user_id"] == reddit_to_eval.author_id("t2_x", "s") and "t2_x" not in row["user_id"]
    senders = [m["sender"] for m in row["messages"]]
    assert senders == ["self", "match", "self", "match", "self", "match", "self"]
    bodies = " ".join(m["body"] for m in row["messages"])
    assert "Jess" not in bodies and "Match" in bodies
    assert "[phone]" in bodies and "[email]" in bodies
    assert "my knees hate me" in row["messages"][4]["body"]
    assert "EDIT" not in bodies and "u/someone" not in bodies


@pytest.mark.parametrize(
    "post",
    [
        _post(selftext="[deleted]"),
        _post(author_fullname="[deleted]"),
        _post(selftext="look at this screenshot lol"),
        _post(selftext="Me: hi\nHer: hey\nMe: sup"),
        _post(selftext=TRANSCRIPT.replace("Me: will do!", "Friend: lol")),
    ],
)
def test_unusable_or_deleted_posts_are_skipped(post):
    assert reddit_to_eval.to_row(post, salt="s") is None


def test_reddit_rows_never_load_for_training(tmp_path):
    raw = tmp_path / "raw.jsonl"
    raw.write_text(json.dumps(_post()) + "\n")
    out = tmp_path / "eval.jsonl"
    assert reddit_to_eval.main(["--in", str(raw), "--out", str(out), "--salt", "s"]) == 0
    assert load_jsonl(out) == []
    assert load_jsonl(out, require_consent=False, exclude_sources=TEST_ONLY_SOURCES) == []
    assert len(load_jsonl(out, require_consent=False)) == 1

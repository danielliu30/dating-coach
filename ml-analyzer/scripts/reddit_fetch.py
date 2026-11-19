#!/usr/bin/env python
"""Fetch dating-conversation posts from Reddit for TESTING the analyzer only.

Reddit's Data API terms restrict commercial use and ML training, so rows built
from these posts are tagged ``source: "reddit"`` and ``train.py`` refuses them
unless ``--allow-source reddit`` is passed. Only run this once Reddit has
granted permission for this use.

Needs a Reddit "script" app (https://www.reddit.com/prefs/apps):

    REDDIT_CLIENT_ID=... REDDIT_CLIENT_SECRET=... REDDIT_USER_AGENT="dating-coach-eval/0.1 by u/<you>" \\
        python scripts/reddit_fetch.py --subreddit Tinder --subreddit hingeapp --query "conversation" --limit 100

Raw posts go to ``data/reddit/raw.jsonl`` (git-ignored). Each run rewrites
the file, so posts deleted on Reddit since the last run disappear locally as
well; ``reddit_to_eval.py`` also skips ``[deleted]``/``[removed]`` content.
Comments are never fetched: crowd advice is not a coaching target.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
from pathlib import Path
from typing import Any, Dict, Iterator, List, Optional, Sequence

import httpx

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_OUT = ROOT / "data" / "reddit" / "raw.jsonl"
TOKEN_URL = "https://www.reddit.com/api/v1/access_token"
API = "https://oauth.reddit.com"
# Only what the converter needs; nothing about the poster beyond a pseudonymous author id.
KEPT_FIELDS = ("id", "subreddit", "title", "selftext", "author_fullname", "created_utc", "is_self", "url", "over_18")
PAGE_SIZE = 100


class MissingCredentials(SystemExit):
    pass


def credentials(env: Optional[Dict[str, str]] = None) -> Dict[str, str]:
    env = dict(os.environ) if env is None else env
    names = ("REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "REDDIT_USER_AGENT")
    missing = [n for n in names if not env.get(n, "").strip()]
    if missing:
        raise MissingCredentials(f"reddit_fetch: set {', '.join(missing)} (Reddit API script app) before fetching")
    return {n: env[n].strip() for n in names}


def access_token(client: httpx.Client, creds: Dict[str, str]) -> str:
    """App-only OAuth token (client-credentials grant); never a user password."""
    response = client.post(
        TOKEN_URL,
        auth=(creds["REDDIT_CLIENT_ID"], creds["REDDIT_CLIENT_SECRET"]),
        data={"grant_type": "client_credentials"},
        headers={"User-Agent": creds["REDDIT_USER_AGENT"]},
    )
    response.raise_for_status()
    return response.json()["access_token"]


def _respect_rate_limit(response: httpx.Response, sleep=time.sleep) -> None:
    remaining = response.headers.get("x-ratelimit-remaining")
    reset = response.headers.get("x-ratelimit-reset")
    if remaining is not None and reset is not None and float(remaining) < 1:
        sleep(float(reset) + 1)


def posts(
    client: httpx.Client,
    token: str,
    user_agent: str,
    subreddit: str,
    query: Optional[str],
    limit: int,
    sort: str = "top",
    time_filter: str = "year",
    sleep=time.sleep,
) -> Iterator[Dict[str, Any]]:
    """Up to ``limit`` self posts from ``subreddit`` (search when ``query`` is set), following ``after``."""
    path = f"/r/{subreddit}/search" if query else f"/r/{subreddit}/{sort}"
    params: Dict[str, Any] = {"limit": PAGE_SIZE, "t": time_filter, "raw_json": 1}
    if query:
        params.update(q=query, restrict_sr=1, sort=sort)
    headers = {"Authorization": f"bearer {token}", "User-Agent": user_agent}
    seen = 0
    while seen < limit:
        response = client.get(API + path, params=params, headers=headers)
        response.raise_for_status()
        _respect_rate_limit(response, sleep)
        data = response.json()["data"]
        for child in data.get("children", []):
            post = child.get("data", {})
            if not post.get("is_self") or post.get("over_18"):
                continue
            yield {k: post.get(k) for k in KEPT_FIELDS}
            seen += 1
            if seen >= limit:
                return
        if not data.get("after"):
            return
        params["after"] = data["after"]


def parse_args(argv: Sequence[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--subreddit", action="append", required=True)
    parser.add_argument("--query", default=None, help="Search within each subreddit instead of listing")
    parser.add_argument("--sort", default="top", choices=["top", "new", "hot", "relevance"])
    parser.add_argument("--time", dest="time_filter", default="year", choices=["day", "week", "month", "year", "all"])
    parser.add_argument("--limit", type=int, default=100, help="Self posts per subreddit")
    parser.add_argument("--out", type=Path, default=DEFAULT_OUT)
    return parser.parse_args(argv)


def main(argv: Sequence[str] | None = None, client: Optional[httpx.Client] = None) -> int:
    args = parse_args(argv)
    creds = credentials()
    client = client or httpx.Client(timeout=30)
    token = access_token(client, creds)
    rows: List[Dict[str, Any]] = []
    for subreddit in args.subreddit:
        rows += posts(client, token, creds["REDDIT_USER_AGENT"], subreddit, args.query, args.limit, args.sort, args.time_filter)
    args.out.parent.mkdir(parents=True, exist_ok=True)
    with args.out.open("w", encoding="utf-8") as handle:
        for row in rows:
            handle.write(json.dumps(row, ensure_ascii=False) + "\n")
    print(f"wrote {len(rows)} post(s) to {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

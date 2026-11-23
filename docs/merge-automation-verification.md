# Merge automation event-attribution check

Verifies that merges armed via MERGE_BOT_TOKEN are attributed to a real
token, so `closed` (cascade) and `push` (release) events fire. Safe to keep
or delete; it documents that the check was done.

-- Where the session happens: a video-call or other join link the coach sets
-- once the booking is confirmed. Empty until then.
ALTER TABLE coaching_sessions
    ADD COLUMN meeting_url text NOT NULL DEFAULT '';

-- Client ratings of coaches. A client may leave one review per coach, and only
-- after a completed session with them (enforced by the service); session_id
-- records which session the review was written against.
CREATE TABLE coach_reviews (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    coach_id   uuid NOT NULL REFERENCES coaches (user_id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    session_id uuid REFERENCES coaching_sessions (id) ON DELETE SET NULL,
    rating     smallint NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment    text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (coach_id, user_id)
);

CREATE INDEX coach_reviews_coach_idx ON coach_reviews (coach_id, created_at DESC);

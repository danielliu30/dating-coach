-- Manual approval gate for coach profiles. A coach starts 'pending' when the
-- profile row is created and only becomes listable/bookable once an admin sets
-- 'approved'. Coaches that existed before this gate keep operating.
ALTER TABLE coaches
    ADD COLUMN approval_status text NOT NULL DEFAULT 'pending'
        CONSTRAINT coaches_approval_status_check
        CHECK (approval_status IN ('pending', 'approved', 'rejected'));

UPDATE coaches SET approval_status = 'approved';

CREATE INDEX coaches_approval_status_idx ON coaches (approval_status);

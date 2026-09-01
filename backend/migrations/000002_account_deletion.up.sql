-- Accounts awaiting the deletion worker keep their row but must not be usable:
-- deleted_at is the durable record that stops sign-in from minting new tokens.
ALTER TABLE users ADD COLUMN deleted_at timestamptz;

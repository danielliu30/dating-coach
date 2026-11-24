-- Runs once on first init of a fresh postgres-data volume (skipped when the
-- volume already has data). Enables pgvector in the app database for RAG
-- embeddings; on an existing volume, run `CREATE EXTENSION IF NOT EXISTS vector`
-- once by hand — the enabled extension then persists with the volume.
CREATE EXTENSION IF NOT EXISTS vector;

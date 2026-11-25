CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE rag_chunks (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            text NOT NULL,
    owner_id        uuid REFERENCES users(id) ON DELETE CASCADE,
    conversation_id uuid REFERENCES conversations(id) ON DELETE CASCADE,
    seq             smallint NOT NULL DEFAULT 0,
    content         text NOT NULL,
    embedding       vector(1536) NOT NULL,
    model           text NOT NULL,
    doc_version     smallint NOT NULL DEFAULT 1,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT rag_chunks_kind_check CHECK (kind IN ('conversation')),
    CONSTRAINT rag_chunks_shape_check CHECK (
        (kind = 'conversation') = (owner_id IS NOT NULL AND conversation_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX rag_chunks_conversation_seq_idx
    ON rag_chunks (conversation_id, seq) WHERE kind = 'conversation';
CREATE INDEX rag_chunks_kind_owner_idx ON rag_chunks (kind, owner_id);
CREATE INDEX rag_chunks_embedding_idx ON rag_chunks
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);

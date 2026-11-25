-- name: FindSimilarConversations :many
SELECT rc.conversation_id, rc.content,
       (rc.embedding <=> @query_embedding::vector)::float8 AS distance
FROM rag_chunks rc
WHERE rc.kind = 'conversation'
  AND rc.owner_id = @owner_id
  AND rc.conversation_id <> @exclude_conversation_id
ORDER BY rc.embedding <=> @query_embedding::vector
LIMIT @max_results;

-- name: UpsertRagChunk :one
INSERT INTO rag_chunks (kind, owner_id, conversation_id, seq, content, embedding, model, doc_version)
VALUES ('conversation', @owner_id, @conversation_id, 0, @content, @embedding::vector, @model, @doc_version)
ON CONFLICT (conversation_id, seq) WHERE kind = 'conversation' DO UPDATE
SET content = EXCLUDED.content, embedding = EXCLUDED.embedding,
    model = EXCLUDED.model, doc_version = EXCLUDED.doc_version, updated_at = now()
RETURNING id, kind, owner_id, conversation_id, seq, content, model, doc_version, created_at, updated_at;

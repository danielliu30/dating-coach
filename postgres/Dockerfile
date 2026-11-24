# Postgres with pgvector for RAG embeddings. Kept on the same Alpine base as
# postgres:16-alpine so musl/glibc collations stay consistent with volumes
# initialized by the stock image — switching to a Debian-based image would
# risk collation mismatch warnings on existing data.
FROM postgres:16-alpine

# The apk package targets Alpine's own postgresql16 layout; build from source
# so the extension lands in this image's /usr/local PGXS paths.
RUN apk add --no-cache --virtual .build-deps build-base git \
    && git clone --branch v0.8.1 --depth 1 https://github.com/pgvector/pgvector /tmp/pgvector \
    && make -C /tmp/pgvector with_llvm=no \
    && make -C /tmp/pgvector install with_llvm=no \
    && rm -rf /tmp/pgvector \
    && apk del .build-deps

COPY initdb/ /docker-entrypoint-initdb.d/

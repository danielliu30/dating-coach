# Builds both entrypoints; the compose services pick one via `command`.
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -o /out/worker ./cmd/worker \
    && CGO_ENABLED=0 go build -trimpath -o /out/metering ./cmd/metering

FROM alpine:3.20 AS metering
RUN apk add --no-cache ca-certificates
# Runs as root: it must read the docker socket (read-only mount) — the socket
# is root/docker-group only, and the service exposes nothing writable.
COPY --from=build /out/metering /usr/local/bin/metering
EXPOSE 9090
CMD ["metering"]

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 app \
    && mkdir -p /logs && chown app:app /logs
COPY --from=build /out/api /usr/local/bin/api
COPY --from=build /out/worker /usr/local/bin/worker
USER app
EXPOSE 8080
CMD ["api"]

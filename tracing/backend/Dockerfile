# Builds ingest-api, query-api, control-api, and processor (same image, different CMD).
FROM golang:1.23-alpine AS builder

ENV GOTOOLCHAIN=auto
RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/ingest-api ./cmd/ingest-api \
 && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/query-api ./cmd/query-api \
 && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/control-api ./cmd/control-api \
 && CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/processor ./cmd/processor

FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl
COPY --from=builder /out/ingest-api /out/query-api /out/control-api /out/processor /usr/local/bin/
EXPOSE 8081 8082 8083
CMD ["ingest-api"]

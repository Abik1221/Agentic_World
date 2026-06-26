# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS build
WORKDIR /src

# Cache module downloads.
COPY go.mod go.sum* ./
RUN go mod download

# Build a static, stripped binary.
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/server ./cmd/server

# ── Runtime stage ────────────────────────────────────────────────────────────────
# distroless static: no shell, no package manager, non-root by default — minimal
# attack surface. CA certs are included for outbound TLS (Stripe/X).
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/server /server
COPY migrations /migrations
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]

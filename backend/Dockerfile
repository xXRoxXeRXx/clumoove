# --- Development Stage ---
FROM golang:alpine AS dev
WORKDIR /app/backend
# Install git and other utilities if needed
RUN apk add --no-cache git build-base
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
RUN go install github.com/air-verse/air@v1.66.0
COPY backend/ ./
COPY frontend/src/locales /app/frontend/src/locales
RUN go generate ./internal/i18n
# API runs by default in dev stage, worker can override via docker-compose command
CMD ["go", "run", "./cmd/api"]

# --- Build Stage ---
FROM --platform=$BUILDPLATFORM golang:alpine AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum* ./
RUN go mod download
COPY backend/ ./
COPY frontend/src/locales /src/frontend/src/locales
RUN go generate ./internal/i18n
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -o /bin/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -o /bin/worker ./cmd/worker

# --- Production Stage ---
FROM alpine:3.19 AS prod
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/api /app/api
COPY --from=builder /bin/worker /app/worker
# Default command runs the API. Worker overrides this.
CMD ["/app/api"]

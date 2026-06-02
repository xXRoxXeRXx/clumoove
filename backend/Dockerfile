# --- Development Stage ---
FROM golang:alpine AS dev
WORKDIR /app
# Install git and other utilities if needed
RUN apk add --no-cache git build-base
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# API runs by default in dev stage, worker can override via docker-compose command
CMD ["go", "run", "cmd/api/main.go"]

# --- Build Stage ---
FROM golang:alpine AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/api cmd/api/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -o /bin/worker cmd/worker/main.go

# --- Production Stage ---
FROM alpine:3.19 AS prod
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/api /app/api
COPY --from=builder /bin/worker /app/worker
# Default command runs the API. Worker overrides this.
CMD ["/app/api"]

# Stage 1: Build the Go binary
FROM golang:1.24-alpine AS builder

# Install gcc and musl-dev because go-sqlite3 requires CGO
RUN apk add --no-cache gcc musl-dev

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy the source code
COPY . .

# Build statically linked binary (CGO_ENABLED=1 is required for sqlite3)
# We use Alpine instead of scratch for the runner because sqlite needs libc
RUN CGO_ENABLED=1 GOOS=linux go build -a -o discordbot .

# Stage 2: Minimal runtime image
FROM alpine:latest

# Install CA certificates to enable HTTPS communication with Discord/Gemini APIs
# Install tzdata for timezone support
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Copy the binary from the builder
COPY --from=builder /app/discordbot .

# Create the data directory for SQLite persistence
RUN mkdir -p /app/data

# Run the binary
CMD ["./discordbot"]

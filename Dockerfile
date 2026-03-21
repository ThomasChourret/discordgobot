# Stage 1: Build the Go binary
FROM golang:1.24-bookworm AS builder

# golang:1.24-bookworm already includes gcc and libc-dev needed for CGO

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
# We use Debian slim instead of Alpine to guarantee 100% glibc compatibility for go-sqlite3 bindings
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y ca-certificates tzdata ffmpeg python3 python3-pip && \
    pip3 install --break-system-packages yt-dlp && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy the binary from the builder
COPY --from=builder /app/discordbot .

# Create the data directory for SQLite persistence
RUN mkdir -p /app/data

# Run the binary
CMD ["./discordbot"]

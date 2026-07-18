# Build in a stock Go builder container
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

# Build a statically-linked binary and take advantage of layer caching
ENV CGO_ENABLED=0

RUN apk add --no-cache git

WORKDIR /src

# Copy module files first to cache dependency download step
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags "-s -w" -o /jellych .

FROM alpine:3.24
RUN apk add --no-cache tzdata ca-certificates ffmpeg

# Create an unprivileged user to run the service
RUN addgroup -S -g 1000 app && adduser -S -D -H -u 1000 -G app app

# Copy only the built binary from the builder stage
COPY --from=builder /jellych /usr/local/bin/jellych

# /data/config stores the persistent jellych.db SQLite database.
RUN mkdir -p /etc/jellych /data/config /data/vods && chown -R app:app /etc/jellych /data

USER app
WORKDIR /etc/jellych

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD ["wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/health"]

ENTRYPOINT ["/usr/local/bin/jellych"]

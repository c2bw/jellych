# Build in a stock Go builder container
FROM golang:1.25-alpine AS builder

# Build a statically-linked binary and take advantage of layer caching
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

RUN apk add --no-cache git

WORKDIR /src

# Copy module files first to cache dependency download step
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source and build
COPY . .
RUN go build -ldflags "-s -w" -o /jellych .

FROM alpine:latest
RUN apk add --no-cache tzdata ca-certificates ffmpeg streamlink

# Create an unprivileged user to run the service
RUN addgroup -S -g 1000 app && adduser -S -D -H -u 1000 -G app app

# Copy only the built binary from the builder stage
COPY --from=builder /jellych /usr/local/bin/jellych

# Copy application assets
COPY --chown=app:app html/ /etc/jellych/html/

RUN mkdir -p /etc/jellych /data/config /data/vods && chown -R app:app /etc/jellych /data

USER app
WORKDIR /etc/jellych

ENTRYPOINT ["/usr/local/bin/jellych"]

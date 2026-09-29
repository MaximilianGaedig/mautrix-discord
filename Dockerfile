FROM golang:1-alpine3.24 AS builder

RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev

COPY . /build
WORKDIR /build
# The package path is given explicitly: this branch moved package main into ./cmd/mautrix-discord,
# so building the repo root finds no Go files at all. build.sh cannot be used here either - it
# appends "$@" after the package path, where go reads -o as another package to build.
RUN go build -o /usr/bin/mautrix-discord ./cmd/mautrix-discord

FROM alpine:3.24

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ffmpeg su-exec ca-certificates olm bash jq curl yq-go lottieconverter

COPY --from=builder /usr/bin/mautrix-discord /usr/bin/mautrix-discord
COPY --from=builder /build/docker-run.sh /docker-run.sh
VOLUME /data

CMD ["/docker-run.sh"]

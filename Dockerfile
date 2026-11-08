# syntax=docker/dockerfile:1
# Builds the image from the source tree (make docker/build). Releases build
# it from prebuilt binaries instead: see Dockerfile.goreleaser.
ARG GO_VERSION=1.27
ARG ALPINE_VERSION=3.24

FROM golang:${GO_VERSION}-alpine${ALPINE_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/tcpulse .

FROM alpine:${ALPINE_VERSION}
COPY --from=build /out/tcpulse /usr/local/bin/tcpulse
COPY LICENSE NOTICE /usr/share/licenses/tcpulse/
COPY LICENSES/ /usr/share/licenses/tcpulse/LICENSES/
USER nobody:nobody
EXPOSE 9100/tcp 9100/udp
ENTRYPOINT ["/usr/local/bin/tcpulse"]

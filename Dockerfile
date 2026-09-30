# syntax=docker/dockerfile:1

# The builder runs on the build machine's own architecture and cross-compiles,
# which is much faster than emulating the target one.
FROM --platform=$BUILDPLATFORM golang:1-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.* *.go ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /mediakeeper .

FROM alpine:3
# ffmpeg (with ffprobe) and mkvtoolnix: tags inside the files and durations
# for the DLNA server. ca-certificates: HTTPS to the catalogues.
RUN apk add --no-cache ca-certificates ffmpeg mkvtoolnix tzdata
COPY --from=build /mediakeeper /usr/local/bin/mediakeeper

# Settings (API keys) live in /config, the library in /media.
ENV XDG_CONFIG_HOME=/config
VOLUME ["/config", "/media"]
EXPOSE 8200/tcp 1900/udp

ENTRYPOINT ["mediakeeper"]
CMD ["-serve", "/media"]

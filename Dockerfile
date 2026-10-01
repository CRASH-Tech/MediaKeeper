# syntax=docker/dockerfile:1

# The builder runs on the build machine's own architecture and cross-compiles,
# which is much faster than emulating the target one.
FROM --platform=$BUILDPLATFORM golang:1-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.* *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /mediakeeper .

FROM alpine:3
# ffmpeg (with ffprobe) and mkvtoolnix: tags inside the files, durations and
# codecs for the clients, conversion for the web player. aria2: torrents and
# magnet links. ca-certificates: HTTPS to the catalogues.
RUN apk add --no-cache aria2 ca-certificates ffmpeg mkvtoolnix tzdata
COPY --from=build /mediakeeper /usr/local/bin/mediakeeper

# Settings (API keys) live in /config, the library in /media. The program's
# own folder is not writable here, so the settings file is named outright,
# where earlier versions of the image kept it.
ENV XDG_CONFIG_HOME=/config MEDIAKEEPER_CONFIG=/config/mediakeeper/config.yaml
VOLUME ["/config", "/media"]
EXPOSE 8200/tcp 1900/udp

ENTRYPOINT ["mediakeeper"]
CMD ["-serve", "/media"]

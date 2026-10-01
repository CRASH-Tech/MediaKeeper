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
# Intel graphics for hardware conversion (MEDIAKEEPER_HWACCEL=auto, vaapi or
# qsv); its drivers exist for x86-64 only. An AMD card needs mesa-va-gallium,
# 200 MB more, so it is not in the image by default:
#   docker build --build-arg EXTRA_PACKAGES=mesa-va-gallium .
# Alpine's ffmpeg has no NVENC: for NVIDIA, run the binary on the host.
ARG EXTRA_PACKAGES=""
RUN if [ "$(apk --print-arch)" = "x86_64" ]; then apk add --no-cache intel-media-driver onevpl-intel-gpu; fi && \
    if [ -n "$EXTRA_PACKAGES" ]; then apk add --no-cache $EXTRA_PACKAGES; fi
COPY --from=build /mediakeeper /usr/local/bin/mediakeeper

# The database (settings, accounts) lives right in /config, the library in
# /media (the server takes it as its library folder on the first start;
# others are chosen in the web interface). The program's own folder is not
# writable here, so the folder is named outright. Earlier versions of the
# image kept everything in /config/mediakeeper: that is still used while it
# is there.
ENV XDG_CONFIG_HOME=/config MEDIAKEEPER_CONFIG=/config
VOLUME ["/config", "/media"]
EXPOSE 8200/tcp 1900/udp

ENTRYPOINT ["mediakeeper"]
CMD ["-serve"]

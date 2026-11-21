# The image every Go tool and test runs in (scripts/go.sh): the pinned Go
# image plus ffmpeg, which the media tests run for real. The ffmpeg version
# is the one in the server image (server/Dockerfile); change both together.
FROM golang:1.27.3-trixie
ARG FFMPEG_VERSION=7:7.1.5-0+deb13u1
RUN apt-get update \
 && apt-get install -y --no-install-recommends "ffmpeg=${FFMPEG_VERSION}" \
 && rm -rf /var/lib/apt/lists/*

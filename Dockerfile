# syntax=docker/dockerfile:1

# Frontend and Go stages run on the build host's platform; Go cross-compiles
# for the target, so multi-arch builds don't need emulation.
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/xwebfm ./cmd/xwebfm

FROM alpine:3.20
# 7zip enables creating .7z archives; extraction needs no external tools.
RUN apk add --no-cache 7zip ca-certificates
COPY --from=build /out/xwebfm /usr/local/bin/xwebfm
EXPOSE 8080
# Runs as root on purpose: managing other users' files is the point. Mount
# the host filesystem at /host and its account files so names resolve.
ENTRYPOINT ["/usr/local/bin/xwebfm"]
CMD ["-listen", "0.0.0.0:8080", "-root", "/host", "-passwd", "/host/etc/passwd", "-group", "/host/etc/group"]

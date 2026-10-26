# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS api
ARG TARGETOS
ARG TARGETARCH
# Release tag injected by the release workflow; local builds report "dev".
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X onefrog/internal/version.Version=${VERSION}" -o /out/server ./cmd/server

FROM alpine:3.23
ARG VERSION=dev
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.source="https://github.com/llc370219/onefrog" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.description="OneFrog: self-hosted AI ops panel"
RUN apk add --no-cache ca-certificates tzdata su-exec \
 && addgroup -S -g 10001 onefrog \
 && adduser -S -D -H -u 10001 -G onefrog onefrog
WORKDIR /app
COPY --from=api /out/server /app/server
COPY --from=web /src/web/dist /app/web/dist
COPY deploy/docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
COPY LICENSE THIRD_PARTY_NOTICES DISCLAIMER.md /usr/share/doc/onefrog/
ENV OF_WEB_DIR=/app/web/dist \
    OF_HTTP_ADDR=:8080 \
    OF_DATA_DIR=/app/data
VOLUME ["/app/data"]
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/api/health || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["/app/server"]

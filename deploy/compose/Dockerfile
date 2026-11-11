# syntax=docker/dockerfile:1
# 中心 meterly-server 镜像:多阶段静态构建,最终镜像为 scratch(只有一个二进制 + CA 证书 + 默认配置)。
# 构建(在仓库根目录):docker compose -f deploy/compose/docker-compose.yml build
# 版本号由 compose 从 .env 的 METERLY_VERSION 传入(镜像 tag 同名),见 deploy/compose/README.md。

ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w -buildid= -X github.com/domoxiaojun/Meterly/internal/version.Version=${VERSION} -X github.com/domoxiaojun/Meterly/internal/version.Commit=${COMMIT} -X github.com/domoxiaojun/Meterly/internal/version.Date=${DATE}" \
      -o /out/meterly-server ./cmd/meterly-server \
 && mkdir -p /out/data/geo /out/data/backups

FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="meterly-server" \
      org.opencontainers.image.description="分布式流量统计中心(ingest + SQLite + 中文面板)" \
      org.opencontainers.image.source="https://github.com/domoxiaojun/Meterly" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/meterly-server /meterly-server
# 命名卷首次挂载时会继承镜像内 /data 的属主(65532),无需手工 chown
COPY --from=build --chown=65532:65532 /out/data /data
COPY deploy/compose/server.json /etc/meterly/server.json
ENV METERLY_CONFIG=/etc/meterly/server.json \
    TMPDIR=/tmp \
    TZ=Asia/Singapore
USER 65532:65532
EXPOSE 8787
VOLUME ["/data"]
# 用二进制自带的 healthcheck 子命令(scratch 里没有 curl/wget/shell)
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/meterly-server", "healthcheck"]
ENTRYPOINT ["/meterly-server"]
CMD ["serve"]

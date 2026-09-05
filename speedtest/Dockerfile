# mmwx-speedtester 容器镜像。
#
# 用法:
#   docker run -d --name mmwx-speedtester --restart unless-stopped \
#     -e MMWX_MASTER=https://your-master.example.com \
#     -e MMWX_SPEEDTEST_TOKEN=<token> \
#     -e MMWX_SPEEDTEST_NAME=home-tester \
#     -v mmwx-speedtester-data:/data \
#     ghcr.io/zzulpc/mmwx-speedtester:0.2.6
#
# Mihomo 与 sing-box 都固定在镜像内；已有 /data 卷可继续挂载，但不再负责提供代理内核。
# 容器运行期只需访问主控 WS 与测速目标，不需要连接 GitHub 下载内核。

# ---- Snell v6 内核 ----
# 固定官方多架构镜像的标签与清单摘要，避免 testing/latest 漂移。
FROM ghcr.io/sagernet/sing-box:v1.14.0@sha256:4bed9332a0013fef72c31200a84e8fc0ed91a5ab2fe373a69f0acbbbbfbef3c5 AS singbox

# ---- Mihomo 内核 ----
# 下载阶段固定在构建机架构运行，但必须按 TARGETARCH 选择目标资产，避免跨架构镜像打入错误二进制。
FROM --platform=$BUILDPLATFORM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS mihomo
ARG TARGETOS
ARG TARGETARCH
RUN set -eux; \
    test "$TARGETOS" = "linux"; \
    case "$TARGETARCH" in \
      amd64) \
        asset="mihomo-linux-amd64-compatible-v1.19.30.gz"; \
        expected="db214c7a2517e63c150d123178d16d102e03a241ccdae4e5e07ffbe9cf56c6f9" ;; \
      arm64) \
        asset="mihomo-linux-arm64-v1.19.30.gz"; \
        expected="58896873736d28628f66de3677c8654fa0f180662523148e136cff4f6e890069" ;; \
      *) echo "不支持的目标架构: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    apk add --no-cache ca-certificates curl; \
    mkdir -p /out; \
    curl --fail --location --retry 3 \
      --output /tmp/mihomo.gz \
      "https://github.com/MetaCubeX/mihomo/releases/download/v1.19.30/${asset}"; \
    echo "${expected}  /tmp/mihomo.gz" | sha256sum -c -; \
    gzip -dc /tmp/mihomo.gz > /out/mihomo; \
    chmod 0555 /out/mihomo

# ---- builder ----
FROM golang:1.26.8-alpine3.24@sha256:ce864e7223ac17b1775e6fd0b4c0db580c2eb50e7953a427916379e4b92a1628 AS builder
WORKDIR /src
# 单独 cp go.mod/sum 让 mod download 缓存层稳定
COPY go.mod go.sum ./
# 标签与摘要同时固定后仍验证实际工具链，防止更新其中一处时构建器版本悄悄落后。
RUN test "$(go env GOVERSION)" = "go1.26.8" && go mod download
COPY . .
# -trimpath + -s -w 缩小 binary;CGO_ENABLED=0 全静态
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mmwx-speedtester .

# ---- runtime ----
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
# ca-certificates:主控 WSS 鉴权与 HTTPS 测速目标
# tzdata:日志时间戳本地化(可选)
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S speedtest && adduser -S -G speedtest speedtest && \
    mkdir -p /data && chown speedtest:speedtest /data

COPY --from=builder /out/mmwx-speedtester /usr/local/bin/mmwx-speedtester
COPY --from=singbox /usr/local/bin/sing-box /usr/local/bin/sing-box
COPY --from=mihomo /out/mihomo /usr/local/bin/mihomo
COPY LICENSES/ /usr/share/licenses/mmwx-speedtester/
COPY THIRD_PARTY_NOTICES.md CORRESPONDING_SOURCE.md /usr/share/doc/mmwx-speedtester/

LABEL org.opencontainers.image.title="mmwx-speedtester" \
      org.opencontainers.image.description="支持 Snell v6 分流的妙妙屋X家庭测速端" \
      org.opencontainers.image.licenses="MIT AND GPL-3.0-only AND GPL-3.0-or-later AND BSD-2-Clause AND Apache-2.0"

USER speedtest
# 构建阶段直接执行最终镜像里的两个内核，避免仅凭 COPY 成功就发布版本错误或不可执行的产物。
RUN set -eu; \
    mihomo_output="$(/usr/local/bin/mihomo -v 2>&1)"; \
    mihomo_version="$(printf '%s\n' "$mihomo_output" | awk 'NR == 1 { print $3 }')"; \
    test "$mihomo_version" = "v1.19.30"; \
    sing_box_output="$(/usr/local/bin/sing-box version 2>&1)"; \
    sing_box_version_line="$(printf '%s\n' "$sing_box_output" | awk 'NR == 1 { print; exit }')"; \
    test "$sing_box_version_line" = "sing-box version 1.14.0"
WORKDIR /data
VOLUME ["/data"]

# 镜像用绝对路径显式指定两个内核，不依赖 PATH 搜索。只有直接运行独立二进制且未设置
# SING_BOX_BIN 时，程序才会依次检查数据目录和 PATH；镜像内的用户可写 /data 不会覆盖固定内核。
ENV MMWX_DATA_DIR=/data \
    MIHOMO_BIN=/usr/local/bin/mihomo \
    SING_BOX_BIN=/usr/local/bin/sing-box

# 主控地址、测速端名称和配对令牌不写默认 ENV，仍强制在运行时显式传入。
ENTRYPOINT ["/usr/local/bin/mmwx-speedtester"]

# Build stage
FROM --platform=$BUILDPLATFORM golang:alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -ldflags="-s -w" -o dvb-web cmd/server/main.go

# Runtime stage: Debian bookworm includes FFmpeg with native NVIDIA NVENC,
# Intel QuickSync/VA-API, AMD Mesa VA-API, and ARM V4L2-M2M hardware acceleration.
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
ENV NVIDIA_VISIBLE_DEVICES=all
ENV NVIDIA_DRIVER_CAPABILITIES=compute,video,utility

RUN sed -i 's/Components: main/Components: main contrib non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        ffmpeg \
        v4l-utils \
        ca-certificates \
        tzdata \
        mesa-va-drivers \
        va-driver-all && \
    case "$(dpkg --print-architecture)" in \
        amd64) apt-get install -y --no-install-recommends intel-media-va-driver-non-free i965-va-driver-shaders || true ;; \
    esac && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /app/dvb-web /app/dvb-web
COPY web /app/web

VOLUME [ "/app/data" ]

EXPOSE 8080
EXPOSE 65001/udp
EXPOSE 20000-20050/udp

ENTRYPOINT [ "/app/dvb-web" ]
CMD [ "-port", "8080", "-data-dir", "/app/data", "-web-dir", "/app/web" ]

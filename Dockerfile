# Build stage
FROM golang:alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o dvb-web cmd/server/main.go

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache \
    ffmpeg \
    v4l-utils \
    ca-certificates \
    tzdata \
    mesa-va-gallium \
    libva-intel-driver

WORKDIR /app

COPY --from=builder /app/dvb-web /app/dvb-web
COPY web /app/web

VOLUME [ "/app/data" ]

EXPOSE 8080
EXPOSE 65001/udp
EXPOSE 20000-20050/udp

ENTRYPOINT [ "/app/dvb-web" ]
CMD [ "-port", "8080", "-data-dir", "/app/data", "-web-dir", "/app/web" ]

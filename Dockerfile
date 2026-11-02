FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/reposter ./cmd/reposter

FROM node:22-bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && npm install -g @openai/codex@0.162.1 \
    && mkdir -p /data/media /app /tmp/reposter-codex \
    && chown -R node:node /data /app /tmp/reposter-codex
WORKDIR /app
COPY --from=build /out/reposter /usr/local/bin/reposter
USER node
ENV MEDIA_DIR=/data/media CODEX_HOME=/tmp/reposter-codex
EXPOSE 8080
CMD ["reposter"]

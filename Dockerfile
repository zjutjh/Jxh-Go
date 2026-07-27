FROM golang:1.25-trixie AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/jxh-bot ./cmd/bot

FROM debian:trixie-slim

WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata curl gosu \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 appuser \
    && useradd --uid 10001 --gid 10001 --create-home --home-dir /app --shell /usr/sbin/nologin appuser
COPY --from=build /out/jxh-bot /usr/local/bin/jxh-bot
COPY config.example.yaml /app/config.yaml
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN mkdir -p /app/data/cache /app/data/flash && chown -R appuser:appuser /app && chmod +x /usr/local/bin/entrypoint.sh

ENV TZ=Asia/Shanghai
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["jxh-bot", "-config", "/app/config.yaml"]

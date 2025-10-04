FROM caddy:2.10.2-builder-alpine AS builder

ENV CGO_ENABLED=1
RUN apk update && apk add gcc brotli-dev musl-dev

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    xcaddy build \
        --with github.com/hmilkovi/caddy-geo-redirect \
        --with github.com/mholt/caddy-ratelimit \
        --with github.com/dunglas/caddy-cbrotli

FROM caddy:2.10.2-alpine
RUN apk add --no-cache brotli-libs
COPY --from=builder /usr/bin/caddy /usr/bin/caddy

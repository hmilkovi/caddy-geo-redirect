FROM caddy:2.10.2-builder-alpine AS builder

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    xcaddy build --with github.com/hmilkovi/caddy-geo-redirect --with github.com/mholt/caddy-ratelimit

FROM caddy:2.10.2-alpine

COPY --from=builder /usr/bin/caddy /usr/bin/caddy

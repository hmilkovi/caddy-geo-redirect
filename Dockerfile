FROM caddy:2.10.2-builder-alpine AS builder

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    xcaddy build \
        --with github.com/hmilkovi/caddy-geo-redirect \
        --with github.com/mholt/caddy-ratelimit

FROM almalinux:10-kitten-minimal

ENV XDG_CONFIG_HOME=/config
ENV XDG_DATA_HOME=/data

RUN mkdir -p \
	/config/caddy \
	/data/caddy \
	/etc/caddy \
	/usr/share/caddy

COPY --from=builder /usr/bin/caddy /usr/bin/caddy
COPY --from=caddy:2.10.2 /etc/caddy/Caddyfile /etc/caddy/Caddyfile
COPY --from=caddy:2.10.2 /usr/share/caddy/index.html /usr/share/caddy/index.html

EXPOSE 80
EXPOSE 443
EXPOSE 443/udp
EXPOSE 2019

WORKDIR /srv

CMD ["caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"]

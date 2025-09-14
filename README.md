# Geo based redirect Caddy Server module
[![MIT licensed][mit-badge]][mit-url]
[![Build Status][actions-badge]][actions-url]

[mit-badge]: https://img.shields.io/badge/license-MIT-blue.svg
[mit-url]: https://github.com/hmilkovi/caddy-geo-redirect/blob/main/LICENSE
[actions-badge]: https://github.com/hmilkovi/caddy-geo-redirect/actions/workflows/ci.yml/badge.svg?branch=main
[actions-url]: https://github.com/hmilkovi/caddy-geo-redirect/actions/workflows/ci.yml

This is a Caddy Server module that calculates Geo distance from client IP to provided domains IP
and then redirect to the closes one to minimize latency. Kinda a alternative for latency based routing DNS.


Example config:
```
{
    debug
    order geo_based_redirect first
}

:8080 {
    geo_based_redirect {
        mmdb_path /usr/local/share/GeoIP/GeoLite2-City.mmdb
        domain_names example.com myapp.net
        max_cache_size 100000
        cache_ttl_seconds 3600
    }
    respond "Hello from the server!"
}
```
- `mmdb_path` sets the file path for the GeoIP database.

- `domain_names` specifies a list of domain names ex. `eu.example.com us.example.com`.

- `max_cache_size` sets the maximum number of entries in the cache, default 100k

- `cache_ttl_seconds` defines the cache entry's time-to-live (TTL) in seconds, default 10 minutes

## Limitations
- Currently supports only IPv4
- Has internal cache of Geo IP lookup so under big load it will start to miss cache and have bigget latency on first request
- Loads GeoIP database in memory so size of it should fit in ram
- CUrrently it doesn't support domains that resolve in multiple IP's (may add it in future)


Example download Geo IP database from [IP Geolocation by DB-IP](https://db-ip.com):
```bash
wget https://download.db-ip.com/free/dbip-city-lite-2025-09.mmdb.gz
```
TO DO: Auto refresh periodically Geo IP database on diks or implemenet update in Redis or Valkey

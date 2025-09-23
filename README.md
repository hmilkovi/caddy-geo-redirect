# Geo based redirect Caddy Server module
[![MIT licensed][mit-badge]][mit-url]
[![Build Status][actions-badge]][actions-url]

[mit-badge]: https://img.shields.io/badge/license-MIT-blue.svg
[mit-url]: https://github.com/hmilkovi/caddy-geo-redirect/blob/main/LICENSE
[actions-badge]: https://github.com/hmilkovi/caddy-geo-redirect/actions/workflows/ci.yml/badge.svg?branch=main
[actions-url]: https://github.com/hmilkovi/caddy-geo-redirect/actions/workflows/ci.yml

This Caddy server module provides a latency-aware alternative to traditional latency-based DNS routing. It works by calculating the geographical distance between the client's IP address and a list of target domain IPs. It then redirects the client to the closest server, minimizing network latency and improving user experience.

## Features:
- From a pool of domains, redirect users to the one with the closest geographical location to minimize latency
- For each domain it periodically check's if DNS A record changed
- For each domain it periodically check's health, slower service is better then dead service
- Ability to periodically sync geo ip database
- Designed with performance in mind, it caches everything it can

Make sure that module is used:
```
xcaddy build --with github.com/hmilkovi/caddy-geo-redirect
```

Example config:
```
{
    debug
    metrics
    order geo_based_redirect first
}

:8080 {
    geo_based_redirect {
        mmdb_uri https://git.io/GeoLite2-City.mmdb
        mmdb_path /usr/local/share/GeoIP/GeoLite2-City.mmdb
        mmdb_download_period_days 14
        domain_names example.com myapp.net
        max_cache_size 100000
        cache_ttl_seconds 3600
        health_uri /ping
    }
    respond "Hello from the server!"
}
```
- `mmdb_uri` optional uri from where we will download geo ip database, also supports gziped mmdb

- `mmdb_download_period_days` download interval in days to download mmdb geo ip database

- `mmdb_path` sets the file path for the GeoIP database.

- `domain_names` specifies a list of domain names ex. `eu.example.com us.example.com`.

- `max_cache_size` sets the maximum number of entries in the cache, default 100k

- `cache_ttl_seconds` defines the cache entry's time-to-live (TTL) in seconds, default 10 minutes

- `health_uri` health check http path ex. `/ping` with linear back-off 1,2,3,4 seconds



## Limitations
- Currently supports only IPv4
- Has internal cache of Geo IP lookup so under big load it will start to miss cache and have bigget latency on first request
- Loads GeoIP database in memory so size of it should fit in ram
- Currently it doesn't support domains that resolve in multiple IP's (may add it in future)


Example download Geo IP city database from [MaxMind's GeoLite2](https://dev.maxmind.com/geoip/geoip2/geolite2/):
```bash
wget https://git.io/GeoLite2-City.mmdb
```

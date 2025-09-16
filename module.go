package caddygeoredirect

import (
	"net"
	"net/http"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"go.uber.org/zap"

	"github.com/hmilkovi/caddy-geo-redirect/geoip"
)

func init() {
	caddy.RegisterModule(Middleware{})
	httpcaddyfile.RegisterHandlerDirective("geo_based_redirect", parseCaddyfile)
}

type Middleware struct {
	MmdbPath        string   `json:"mmdb_path,omitempty"`
	DomainNames     []string `json:"domain_names,omitempty"`
	MaxCacheSize    int      `json:"max_cache_size,omitempty"`
	CacheTTLSeconds int      `json:"cache_ttl_seconds,omitempty"`
	GeoIP           *geoip.GeoIpDatabase
	logger          *zap.Logger
}

func (Middleware) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{
		ID:  "http.handlers.geo_based_redirect",
		New: func() caddy.Module { return new(Middleware) },
	}
}

func (m *Middleware) Provision(ctx caddy.Context) error {
	m.logger = ctx.Logger()

	if m.MaxCacheSize == 0 {
		m.MaxCacheSize = 100000 // default 100k
	}

	if m.CacheTTLSeconds == 0 {
		m.CacheTTLSeconds = 60 * 10 // default 10 minutes
	}

	var err error
	m.GeoIP, err = geoip.NewGeoIpDatabase(m.MmdbPath, m.MaxCacheSize, m.DomainNames)
	if err != nil {
		return err
	}
	m.GeoIP.StartDomainLocationUpdater(time.Hour)
	m.GeoIP.StartCacheCleanup()

	return nil
}

func (m *Middleware) Validate() error {
	for _, domain := range m.DomainNames {
		_, err := net.LookupIP(domain)
		if err != nil {
			return err
		}
	}

	if _, err := os.Stat(m.MmdbPath); os.IsNotExist(err) {
		return err
	}

	return nil
}

func (m Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// Someone is spoofing host header so skip it
	if !slices.Contains(m.DomainNames, r.Host) {
		m.logger.Debug("Host not in domains list", zap.String("host", r.Host))
		return next.ServeHTTP(w, r)
	}

	clientIP, err := netip.ParseAddr(r.RemoteAddr)
	if err != nil {
		m.logger.Error("Can't parse remote address", zap.Error(err), zap.String("ip", r.RemoteAddr))
		return next.ServeHTTP(w, r)
	}

	// We do not support IPv6 so we just skip it
	if clientIP.Is6() && clientIP.IsPrivate() {
		m.logger.Debug("Found IPv6 or private ip skipping redirect check", zap.String("ip", clientIP.String()))
		return next.ServeHTTP(w, r)
	}

	redirectDomain, err := m.GeoIP.GetDomainWithSmallestGeoDistance(
		&clientIP,
		m.CacheTTLSeconds,
	)

	if err != nil {
		m.logger.Error("failed to get ip distance", zap.Error(err))
		return next.ServeHTTP(w, r)
	}

	if redirectDomain != r.Host {
		m.logger.Debug("Found domain that has smaller latency", zap.String("domain", redirectDomain))
		redirectFullUrl := r.URL
		redirectFullUrl.Host = redirectDomain
		redirectFullUrlStr := redirectFullUrl.String()
		m.logger.Debug("Redirecting to", zap.String("url", redirectFullUrlStr), zap.Uint64("cache_len", m.GeoIP.CacheLen.Load()))
		http.Redirect(w, r, redirectFullUrlStr, http.StatusFound)
	}

	return next.ServeHTTP(w, r)
}

func (m *Middleware) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	for d.Next() {
		if d.NextArg() {
			return d.ArgErr()
		}
		for d.NextBlock(0) {
			switch d.Val() {
			case "mmdb_path":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.MmdbPath = d.Val()
			case "domain_names":
				m.DomainNames = d.RemainingArgs()
				if len(m.DomainNames) == 0 {
					return d.ArgErr()
				}
			case "max_cache_size":
				if !d.NextArg() {
					return d.ArgErr()
				}
				size, err := strconv.Atoi(d.Val())
				if err != nil {
					return d.Errf("invalid integer for max_cache_size: %v", err)
				}
				m.MaxCacheSize = size
			case "cache_ttl_seconds":
				if !d.NextArg() {
					return d.ArgErr()
				}
				ttlSeconds, err := strconv.Atoi(d.Val())
				if err != nil {
					return d.Errf("invalid integer for cache_ttl_seconds: %v", err)
				}
				m.CacheTTLSeconds = ttlSeconds
			default:
				return d.Errf("unrecognized subdirective '%s'", d.Val())
			}
		}
	}
	return nil
}

func parseCaddyfile(h httpcaddyfile.Helper) (caddyhttp.MiddlewareHandler, error) {
	var m Middleware
	err := m.UnmarshalCaddyfile(h.Dispenser)
	return m, err
}

var (
	_ caddy.Provisioner           = (*Middleware)(nil)
	_ caddy.Validator             = (*Middleware)(nil)
	_ caddyhttp.MiddlewareHandler = (*Middleware)(nil)
	_ caddyfile.Unmarshaler       = (*Middleware)(nil)
)

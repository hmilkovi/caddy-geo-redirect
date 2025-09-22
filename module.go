package caddygeoredirect

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/caddyserver/caddy/v2/caddyconfig/httpcaddyfile"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/hmilkovi/caddy-geo-redirect/geoip"
)

func init() {
	caddy.RegisterModule(Middleware{})
	httpcaddyfile.RegisterHandlerDirective("geo_based_redirect", parseCaddyfile)
}

type Middleware struct {
	MmdbPath               string   `json:"mmdb_path,omitempty"`
	MmdbUri                string   `json:"mmdb_uri,omitempty"`
	MmdbDownloadPeriodDays int      `json:"mmdb_download_period_days,omitempty"`
	DomainNames            []string `json:"domain_names,omitempty"`
	MaxCacheSize           int      `json:"max_cache_size,omitempty"`
	CacheTTLSeconds        int      `json:"cache_ttl_seconds,omitempty"`
	HealthUri              string   `json:"health_uri,omitempty"`
	GeoIP                  *geoip.GeoIpDatabase
	logger                 *zap.Logger
	redirectCounterMetrics *prometheus.CounterVec
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
		m.MaxCacheSize = 100000
	}

	if m.CacheTTLSeconds == 0 {
		m.CacheTTLSeconds = 60 * 10
	}

	if m.MmdbDownloadPeriodDays == 0 {
		m.MmdbDownloadPeriodDays = 30
	}

	var err error
	m.GeoIP, err = geoip.NewGeoIpDatabase(
		&geoip.NewGeoIpDatabaseArgs{
			Logger:                   m.logger,
			MmdbPathUri:              m.MmdbUri,
			MmdbPath:                 m.MmdbPath,
			MmdbPeriodicDownloadDays: m.MmdbDownloadPeriodDays,
			MaxCacheSize:             m.MaxCacheSize,
			HostingDomains:           m.DomainNames,
			HealthUri:                m.HealthUri,
		},
	)
	if err != nil {
		return err
	}
	m.GeoIP.StartDomainLocationAndHeathCheckUpdater(time.Hour)
	m.GeoIP.StartCacheCleanup()

	if m.MmdbUri != "" && m.MmdbDownloadPeriodDays > 0 {
		m.GeoIP.StartPeriodicGeoDBSyncer()
	}

	m.redirectCounterMetrics = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "geo_based_redirect",
			Help: "Number of redirects to closer server",
		},
		[]string{"status"},
	)
	m.redirectCounterMetrics.WithLabelValues("failed")
	m.redirectCounterMetrics.WithLabelValues("success")
	ctx.GetMetricsRegistry().MustRegister(m.redirectCounterMetrics)

	return nil
}

func (m *Middleware) Validate() error {
	for _, domain := range m.DomainNames {
		_, err := net.LookupIP(domain)
		if err != nil {
			return err
		}
	}

	if m.HealthUri != "" {
		if _, err := url.ParseRequestURI(m.HealthUri); err != nil {
			return err
		}
	}

	if _, err := os.Stat(m.MmdbPath); os.IsNotExist(err) && m.MmdbUri == "" {
		return err
	}

	return nil
}

func (m Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	// We don't want to redirect on health check path
	if r.URL.Path == m.HealthUri {
		return next.ServeHTTP(w, r)
	}

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
		m.redirectCounterMetrics.WithLabelValues("failed").Inc()
		return next.ServeHTTP(w, r)
	}

	if redirectDomain != r.Host {
		m.logger.Debug("Found domain that has smaller latency", zap.String("domain", redirectDomain))
		redirectFullUrl := r.URL
		redirectFullUrl.Host = redirectDomain
		redirectFullUrlStr := redirectFullUrl.String()
		m.logger.Debug("Redirecting to", zap.String("url", redirectFullUrlStr), zap.Uint64("cache_len", m.GeoIP.CacheLen.Load()))
		m.redirectCounterMetrics.WithLabelValues("success").Inc()
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
			case "mmdb_uri":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.MmdbUri = d.Val()
			case "mmdb_download_period_days":
				if !d.NextArg() {
					return d.ArgErr()
				}
				size, err := strconv.Atoi(d.Val())
				if err != nil {
					return d.Errf("invalid integer for mmdb_download_period_days: %v", err)
				}
				m.MmdbDownloadPeriodDays = size
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
			case "health_uri":
				if !d.NextArg() {
					return d.ArgErr()
				}
				m.HealthUri = d.Val()
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

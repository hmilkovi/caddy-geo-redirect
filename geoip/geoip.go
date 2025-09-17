package geoip

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oschwald/geoip2-golang/v2"
	"go.uber.org/zap"
)

type GeoLocation struct {
	Lat  float64
	Long float64
}

type GeoCacheEntry struct {
	Domain     string
	TTLSec     int
	InsertTime time.Time
}

type DomainGeoLocation struct {
	GeoLocation
	IsAlive bool
}

type GeoIpDatabase struct {
	database            *geoip2.Reader
	cache               sync.Map
	CacheLen            *atomic.Uint64
	maxCacheSize        int
	domainLocations     map[string]*DomainGeoLocation
	domainLocationsLock sync.RWMutex
	hostingDomains      []string
	healthUri           string
	logger              *zap.Logger
}

// GeoIpDatabase loads mmdb in memory so we can reuse it
// if mmdbPath is empty string we will by default use in memory DB-IP and download it every month
func NewGeoIpDatabase(logger *zap.Logger, mmdbPath string, maxCacheSize int, hostingDomains []string, healthUri string) (*GeoIpDatabase, error) {
	var db *geoip2.Reader

	db, err := geoip2.Open(mmdbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load geoip db: %w", err)
	}

	geoIpDb := &GeoIpDatabase{
		database:        db,
		maxCacheSize:    maxCacheSize,
		hostingDomains:  hostingDomains,
		domainLocations: make(map[string]*DomainGeoLocation),
		CacheLen:        &atomic.Uint64{},
		healthUri:       healthUri,
		logger:          logger,
	}

	geoIpDb.updateDomainLocations()
	geoIpDb.updateDomainHealthState()

	return geoIpDb, nil
}

// updateDomainHealthState makes a health check request to domain
func (g *GeoIpDatabase) updateDomainHealthState() {
	client := &http.Client{
		Timeout: 4 * time.Second,
	}

	newLocations := make(map[string]*DomainGeoLocation)
	for _, domain := range g.hostingDomains {
		g.domainLocationsLock.RLock()
		location, exists := g.domainLocations[domain]
		g.domainLocationsLock.RUnlock()

		if !exists {
			continue
		}

		uri := &url.URL{
			Scheme: "http",
			Host:   domain,
			Path:   g.healthUri,
		}
		resp, err := client.Get(uri.String())

		if err != nil {
			location.IsAlive = false
			newLocations[domain] = location
			g.logger.Error("failed helath check", zap.String("domain", domain), zap.Error(err))
			continue
		}

		if resp == nil {
			continue
		}

		if resp.StatusCode > 200 && resp.StatusCode < 400 {
			location.IsAlive = true
		} else {
			location.IsAlive = false
			g.logger.Error("failed helath check", zap.String("domain", domain), zap.Int("code", resp.StatusCode))
		}

		newLocations[domain] = location
	}

	g.domainLocationsLock.Lock()
	g.domainLocations = newLocations
	g.domainLocationsLock.Unlock()
}

// updateDomainLocations is updating the domain location cache.
func (g *GeoIpDatabase) updateDomainLocations() {
	newLocations := make(map[string]*DomainGeoLocation)
	for _, domain := range g.hostingDomains {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		hostIps, err := net.DefaultResolver.LookupIP(ctx, "ip4", domain)
		if err != nil {
			g.logger.Error("failed to resolve domain", zap.String("domain", domain))
			continue
		}

		if len(hostIps) == 0 {
			continue
		}

		ips := make([]netip.Addr, 0, len(hostIps))
		for _, ip := range hostIps {
			hostNetIp, ok := netip.AddrFromSlice(ip)
			if !ok {
				continue
			}

			ips = append(ips, hostNetIp)
		}

		if len(ips) == 0 {
			continue
		}

		loc, err := g.getIPLatLong(&ips[0])
		if err != nil {
			g.logger.Error("failed to get location of ip", zap.String("ip", ips[0].String()))
			continue
		}

		newLoc := DomainGeoLocation{
			IsAlive: true,
		}
		newLoc.GeoLocation.Lat = loc.Lat
		newLoc.GeoLocation.Long = loc.Long
		newLocations[domain] = &newLoc
	}

	g.domainLocationsLock.Lock()
	g.domainLocations = newLocations
	g.domainLocationsLock.Unlock()
}

// StartDomainLocationUpdater starts background process to periodically refresh domain locations and health check them
func (g *GeoIpDatabase) StartDomainLocationAndHeathCheckUpdater(updateInterval time.Duration) {
	if updateInterval < 30*time.Second {
		updateInterval = 30 * time.Second
	}

	tickerLoc := time.NewTicker(updateInterval)
	go func() {
		for range tickerLoc.C {
			g.updateDomainLocations()
			g.updateDomainHealthState()
		}
	}()
}

// StartCacheCleanup start the cleanup process for caches that clears cache every 10sec
func (g *GeoIpDatabase) StartCacheCleanup() {
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {
			g.cache.Range(func(key, value any) bool {
				entry := value.(GeoCacheEntry)
				if time.Since(entry.InsertTime).Seconds() > float64(entry.TTLSec) {
					g.cache.Delete(key)
					g.CacheLen.Add(^uint64(0))
				}
				return true
			})
		}
	}()
}

// getIPLatLong lookups up lat,long geo data from mmdb database
func (g *GeoIpDatabase) getIPLatLong(ip *netip.Addr) (*GeoLocation, error) {
	record, err := g.database.City(*ip)
	if err != nil {
		return nil, fmt.Errorf("failed ip lookup: %w", err)
	}

	if !record.HasData() {
		return nil, fmt.Errorf("no data found for this IP: %s", ip.String())
	}

	return &GeoLocation{
		Lat:  *record.Location.Latitude,
		Long: *record.Location.Longitude,
	}, nil
}

// GetDomainWithSmallestGeoDistance returns domain name with smallest geo distance of ip it resolves and client ip
func (g *GeoIpDatabase) GetDomainWithSmallestGeoDistance(clientIp *netip.Addr, cacheTTLSec int) (string, error) {
	if cacheTTLSec < 10 {
		return "", fmt.Errorf("cache ttl can't be lower then 10 seconds: %d", cacheTTLSec)
	}

	clientIpStr := clientIp.String()
	inCache, exists := g.cache.Load(clientIpStr)

	if exists {
		return inCache.(GeoCacheEntry).Domain, nil
	}

	clientLocation, err := g.getIPLatLong(clientIp)
	if err != nil {
		return "", fmt.Errorf("failed to get client location: %w", err)
	}

	var bestDomain string
	minDistance := math.MaxFloat64

	g.domainLocationsLock.RLock()
	defer g.domainLocationsLock.RUnlock()

	for domain, hostLocation := range g.domainLocations {
		if hostLocation == nil {
			continue
		}

		if !hostLocation.IsAlive {
			continue
		}

		distance := HaversineDistance(
			clientLocation.Lat,
			clientLocation.Long,
			hostLocation.Lat,
			hostLocation.Long,
		)

		if distance < minDistance {
			minDistance = distance
			bestDomain = domain
		}
	}

	if bestDomain == "" {
		return "", fmt.Errorf("all %d domains seem to be down", len(g.domainLocations))
	}

	if int(g.CacheLen.Load()) <= g.maxCacheSize {
		g.CacheLen.Add(1)
		g.cache.Store(
			clientIp.String(),
			GeoCacheEntry{
				Domain:     bestDomain,
				TTLSec:     cacheTTLSec,
				InsertTime: time.Now().UTC(),
			},
		)
	}

	return bestDomain, nil
}

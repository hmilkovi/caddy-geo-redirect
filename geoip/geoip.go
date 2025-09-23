package geoip

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
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

type MMDBLocation struct {
	Location struct {
		Latitude  float64 `maxminddb:"latitude"`
		Longitude float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

type GeoIpDatabase struct {
	databasePath           string
	database               *maxminddb.Reader
	databaseUri            string
	databaseLock           sync.RWMutex
	periodicDbDownloadDays int
	cache                  sync.Map
	CacheLen               *atomic.Uint64
	maxCacheSize           int
	domainLocations        map[string]*DomainGeoLocation
	domainLocationsLock    sync.RWMutex
	hostingDomains         []string
	healthUri              string
	logger                 *zap.Logger
}

type NewGeoIpDatabaseArgs struct {
	Logger                   *zap.Logger
	MmdbPathUri              string
	MmdbPath                 string
	MmdbPeriodicDownloadDays int
	MaxCacheSize             int
	HostingDomains           []string
	HealthUri                string
}

// GeoIpDatabase loads mmdb in memory so we can reuse it
// if mmdbPath is empty string we will by default use in memory DB-IP and download it every month
func NewGeoIpDatabase(args *NewGeoIpDatabaseArgs) (*GeoIpDatabase, error) {
	geoIpDb := &GeoIpDatabase{
		databasePath:           args.MmdbPath,
		databaseUri:            args.MmdbPathUri,
		periodicDbDownloadDays: args.MmdbPeriodicDownloadDays,
		maxCacheSize:           args.MaxCacheSize,
		hostingDomains:         args.HostingDomains,
		domainLocations:        make(map[string]*DomainGeoLocation),
		CacheLen:               &atomic.Uint64{},
		healthUri:              args.HealthUri,
		logger:                 args.Logger,
	}

	if err := geoIpDb.syncDatabase(); err != nil {
		return nil, err
	}

	geoIpDb.updateDomainLocations()

	return geoIpDb, nil
}

// syncDatabase checks if mmdb can and should be downloaded then reads it from disk
func (g *GeoIpDatabase) syncDatabase() error {
	dbFilestat, err := os.Stat(g.databasePath)
	shouldDownload := false
	if err != nil {
		shouldDownload = true
	} else if dbFilestat.IsDir() {
		shouldDownload = true
	} else if time.Since(dbFilestat.ModTime()).Hours()/24 >= float64(g.periodicDbDownloadDays) {
		shouldDownload = true
	}

	if g.databaseUri == "" {
		shouldDownload = false
	}

	if g.periodicDbDownloadDays == 0 {
		shouldDownload = false
	}

	if shouldDownload {
		if err := downloadGeoDB(g.databaseUri, g.databasePath); err != nil {
			return err
		}
	}

	db, err := maxminddb.Open(g.databasePath)
	if err != nil {
		return fmt.Errorf("failed to load geoip db: %w", err)
	}

	g.databaseLock.Lock()
	g.database = db
	g.databaseLock.Unlock()

	return nil
}

func (g *GeoIpDatabase) StartPeriodicGeoDBSyncer() {
	tickerLoc := time.NewTicker(24 * time.Hour)
	go func() {
		for range tickerLoc.C {
			if err := g.syncDatabase(); err != nil {
				g.logger.Error("failed to sync geo ip database", zap.Error(err))
			}
		}
	}()
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
			g.logger.Error("failed health check", zap.String("domain", domain), zap.Error(err))
			continue
		}

		if resp == nil {
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			location.IsAlive = true
		} else {
			location.IsAlive = false
			g.logger.Error("failed health check", zap.String("domain", domain), zap.Int("code", resp.StatusCode))
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
	g.databaseLock.RLock()
	defer g.databaseLock.RUnlock()

	var record MMDBLocation
	if err := g.database.Lookup(*ip).Decode(&record); err != nil {
		return nil, fmt.Errorf("failed ip lookup: %w", err)
	}

	return &GeoLocation{
		Lat:  record.Location.Latitude,
		Long: record.Location.Longitude,
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

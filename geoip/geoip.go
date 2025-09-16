package geoip

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oschwald/geoip2-golang/v2"
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
	Domain   string
	Location *GeoLocation
}

type GeoIpDatabase struct {
	database            *geoip2.Reader
	cache               sync.Map
	CacheLen            *atomic.Uint64
	maxCacheSize        int
	domainLocations     map[string]*GeoLocation
	domainLocationsLock sync.RWMutex
	hostingDomains      []string
}

// GeoIpDatabase loads mmdb in memory so we can reuse it
// if mmdbPath is empty string we will by default use in memory DB-IP and download it every month
func NewGeoIpDatabase(mmdbPath string, maxCacheSize int, hostingDomains []string) (*GeoIpDatabase, error) {
	var db *geoip2.Reader

	db, err := geoip2.Open(mmdbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load geoip db: %w", err)
	}

	geoIpDb := &GeoIpDatabase{
		database:        db,
		maxCacheSize:    maxCacheSize,
		hostingDomains:  hostingDomains,
		domainLocations: make(map[string]*GeoLocation),
		CacheLen:        &atomic.Uint64{},
	}

	geoIpDb.updateDomainLocations()

	return geoIpDb, nil
}

// updateDomainLocations is updating the domain location cache.
func (g *GeoIpDatabase) updateDomainLocations() {
	// We do the lookups first, so we hold the write lock for the shortest time possible.
	newLocations := make(map[string]*GeoLocation)
	for _, domain := range g.hostingDomains {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		hostIps, err := net.DefaultResolver.LookupIP(ctx, "ip4", domain)
		if err != nil {
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
			continue
		}

		newLocations[domain] = loc
	}

	g.domainLocationsLock.Lock()
	g.domainLocations = newLocations
	g.domainLocationsLock.Unlock()
}

// StartDomainLocationUpdater starts background process to periodically refresh domain locations.
func (g *GeoIpDatabase) StartDomainLocationUpdater(updateInterval time.Duration) {
	if updateInterval < 1*time.Minute {
		updateInterval = 1 * time.Minute
	}

	ticker := time.NewTicker(updateInterval)
	go func() {
		for range ticker.C {
			g.updateDomainLocations()
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

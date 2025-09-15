package geoip

import (
	"fmt"
	"math"
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

// A struct to hold results from our concurrent lookups
type distanceResult struct {
	domain   string
	distance float64
	err      error
}

type GeoIpDatabase struct {
	database       *geoip2.Reader
	cache          sync.Map
	CacheLen       *atomic.Uint64
	maxCacheSize   int
	dnsClient      *DnsResolver
	hostingDomains []string
}

// GeoIpDatabase loads mmdb in memory so we can reuse it
// if mmdbPath is empty string we will by default use in memory DB-IP and download it every month
func NewGeoIpDatabase(mmdbPath string, maxCacheSize int, hostingDomains []string) (*GeoIpDatabase, error) {
	var db *geoip2.Reader

	db, err := geoip2.Open(mmdbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load geoip db: %w", err)
	}

	return &GeoIpDatabase{
		database:       db,
		maxCacheSize:   maxCacheSize,
		dnsClient:      &DnsResolver{},
		hostingDomains: hostingDomains,
	}, nil
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

// DistanceFromClientIPtoDomainHost returns geo distance clietn IP and domain name geo locations in km on earth
func (g *GeoIpDatabase) DistanceFromClientIPtoDomainHost(clientLocation *GeoLocation, domainName string) (float64, error) {
	hostIp, err := g.dnsClient.Resolve(domainName, 600)
	if err != nil {
		return 0, err
	}

	hostLocation, err := g.getIPLatLong(hostIp)
	if err != nil {
		return 0, err
	}

	return HaversineDistance(
		clientLocation.Lat,
		clientLocation.Long,
		hostLocation.Lat,
		hostLocation.Long,
	), nil
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

	resultsChan := make(chan distanceResult, len(g.hostingDomains))
	var wg sync.WaitGroup

	wg.Add(len(g.hostingDomains))
	for _, domain := range g.hostingDomains {
		wg.Add(1)
		go func(d string) {
			defer wg.Done()
			distance, err := g.DistanceFromClientIPtoDomainHost(clientLocation, d)
			resultsChan <- distanceResult{domain: d, distance: distance, err: err}
		}(domain)
	}

	wg.Wait()
	close(resultsChan)

	bestResult := distanceResult{
		distance: math.MaxFloat64,
		domain:   "",
		err:      nil,
	}
	for result := range resultsChan {
		if result.err != nil {
			return "", err
		}

		if result.distance < bestResult.distance {
			bestResult = result
		}
	}

	if int(g.CacheLen.Load()) <= g.maxCacheSize {
		g.CacheLen.Add(1)
		g.cache.Store(
			clientIp.String(),
			GeoCacheEntry{
				Domain:     bestResult.domain,
				TTLSec:     cacheTTLSec,
				InsertTime: time.Now().UTC(),
			},
		)
	}

	return bestResult.domain, nil
}

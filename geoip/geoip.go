package geoip

import (
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

type geoDistanceCacheEntry struct {
	Domain     string
	TTLSeconds int
	InsertTime time.Time
}

type dnsCacheEntry struct {
	Ip         netip.Addr
	InsertTime time.Time
}

type GeoIpDatabase struct {
	database            *geoip2.Reader
	geoDistanceCache    sync.Map
	GeoDistanceCacheLen *atomic.Uint64
	maxCacheSize        int
	dnsCache            sync.Map
}

// LoadDatabase loads mmdb in memory so we can reuse it
// if mmdbPath is empty string we will by default use in memory DB-IP and download it every month
func LoadDatabase(mmdbPath string, maxCacheSize int) (*GeoIpDatabase, error) {
	var db *geoip2.Reader

	db, err := geoip2.Open(mmdbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load geoip db: %w", err)
	}

	return &GeoIpDatabase{
		database:     db,
		maxCacheSize: maxCacheSize,
	}, nil
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

// HaversineDistance calculates the distance between two points on Earth.
func HaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	// Convert degrees to radians
	lat1Rad := lat1 * math.Pi / 180
	lon1Rad := lon1 * math.Pi / 180
	lat2Rad := lat2 * math.Pi / 180
	lon2Rad := lon2 * math.Pi / 180

	// Calculate the difference in coordinates
	diffLat := lat2Rad - lat1Rad
	diffLon := lon2Rad - lon1Rad

	// Apply the Haversine formula
	a := math.Pow(math.Sin(diffLat/2), 2) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Pow(math.Sin(diffLon/2), 2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	// earthRadiusKm is the mean radius of Earth in kilometers is 6371.0
	return 6371.0 * c
}

// DistanceFromClientIPtoDomainHost returns geo distance clietn IP and domain name geo locations in km on earth
func (g *GeoIpDatabase) DistanceFromClientIPtoDomainHost(clientIp *netip.Addr, domainName string) (float64, error) {
	clientLocation, err := g.getIPLatLong(clientIp)
	if err != nil {
		return -1, err
	}

	var hostIp netip.Addr
	cachedDNSEntry, exists := g.dnsCache.Load(domainName)
	if exists {
		hostIp = cachedDNSEntry.(dnsCacheEntry).Ip
		if time.Since(cachedDNSEntry.(dnsCacheEntry).InsertTime).Seconds() > 10 {
			g.dnsCache.Delete(domainName)
		}
	} else {
		hostIps, err := net.LookupIP(domainName)
		if err != nil {
			return -1, err
		}

		hostIpAddr, ok := netip.AddrFromSlice(hostIps[0])
		if !ok {
			return -1, fmt.Errorf("failed to convert net.IP to netip.Addr: %s", hostIps[0].String())
		}
		hostIp = hostIpAddr

		g.dnsCache.Store(
			domainName,
			dnsCacheEntry{
				Ip:         hostIpAddr,
				InsertTime: time.Now().UTC(),
			},
		)
	}

	hostLocation, err := g.getIPLatLong(&hostIp)
	if err != nil {
		return -1, err
	}

	return HaversineDistance(
		clientLocation.Lat,
		clientLocation.Long,
		hostLocation.Lat,
		hostLocation.Long,
	), nil
}

// GetDomainWithSmallestGeoDistance returns domain name with smallest geo distance of ip it resolves and client ip
func (g *GeoIpDatabase) GetDomainWithSmallestGeoDistance(clientIp *netip.Addr, domainNames []string, cacheTTLSec int) (string, error) {
	if len(domainNames) == 0 {
		return "", fmt.Errorf("domain name list can not be empty, len: %d", len(domainNames))
	}

	clientIpStr := clientIp.String()
	inCache, exists := g.geoDistanceCache.Load(clientIpStr)

	defer func() {
		g.geoDistanceCache.Range(func(key, value any) bool {
			entry := value.(geoDistanceCacheEntry)
			if time.Since(entry.InsertTime).Seconds() > float64(entry.TTLSeconds) {
				g.geoDistanceCache.Delete(key)
				g.GeoDistanceCacheLen.Swap(g.GeoDistanceCacheLen.Load() - 1)
			}
			return true
		})
	}()

	if exists {
		return inCache.(geoDistanceCacheEntry).Domain, nil
	}

	currentDomain := ""
	currentDistance := math.MaxFloat64
	for _, domain := range domainNames {
		distance, err := g.DistanceFromClientIPtoDomainHost(clientIp, domain)
		if err != nil {
			return "", err
		}

		if distance < currentDistance {
			currentDistance = distance
			currentDomain = domain
		}
	}

	if int(g.GeoDistanceCacheLen.Load()) <= g.maxCacheSize {
		g.GeoDistanceCacheLen.Add(1)
		g.geoDistanceCache.Store(
			clientIp.String(),
			geoDistanceCacheEntry{
				Domain:     currentDomain,
				TTLSeconds: cacheTTLSec,
				InsertTime: time.Now().UTC(),
			},
		)
	}

	return currentDomain, nil
}

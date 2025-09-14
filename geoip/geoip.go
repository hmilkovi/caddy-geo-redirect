package geoip

import (
	"fmt"
	"math"
	"net"
	"net/netip"
	"time"

	"github.com/oschwald/geoip2-golang/v2"
)

type GeoLocation struct {
	Lat  float64
	Long float64
}

type cacheSmallestGeoDistanceEntry struct {
	Domain     string
	TTLSeconds int
	InsertTime time.Time
}

type dnsCacheEntry struct {
	Ip         netip.Addr
	InsertTime time.Time
}

type GeoIpDatabase struct {
	database         *geoip2.Reader
	geoDistancecache map[string]cacheSmallestGeoDistanceEntry
	maxCacheSize     int
	dnsCache         map[string]dnsCacheEntry
}

// LoadDatabase loads mmdb in memory so we can reuse it
func LoadDatabase(mmdbPath string, maxCacheSize int) (*GeoIpDatabase, error) {
	db, err := geoip2.Open(mmdbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load geoip db: %w", err)
	}

	return &GeoIpDatabase{
		database:         db,
		geoDistancecache: make(map[string]cacheSmallestGeoDistanceEntry),
		maxCacheSize:     maxCacheSize,
		dnsCache:         make(map[string]dnsCacheEntry),
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
	cachedDNSEntry, exists := g.dnsCache[domainName]
	if exists {
		hostIp = cachedDNSEntry.Ip
		if time.Since(cachedDNSEntry.InsertTime).Hours() > 1 {
			delete(g.dnsCache, domainName)
		}
	} else {
		hostIps, err := net.LookupIP(domainName)
		if err != nil {
			return -1, fmt.Errorf("failed to resolve %s: %w", domainName, err)
		}

		hostIpAddr, ok := netip.AddrFromSlice(hostIps[0])
		if !ok {
			return -1, fmt.Errorf("failed to convert net.IP to netip.Addr: %s", hostIps[0].String())
		}
		hostIp = hostIpAddr

		g.dnsCache[domainName] = dnsCacheEntry{
			Ip:         hostIpAddr,
			InsertTime: time.Now().UTC(),
		}
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

	inCache, exists := g.geoDistancecache[clientIp.String()]

	defer func() {
		for key, val := range g.geoDistancecache {
			if time.Since(val.InsertTime).Seconds() > float64(val.TTLSeconds) {
				delete(g.geoDistancecache, key)
			}
		}
	}()

	if exists {
		return inCache.Domain, nil
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

	if len(g.geoDistancecache) <= g.maxCacheSize {
		g.geoDistancecache[clientIp.String()] = cacheSmallestGeoDistanceEntry{
			Domain:     currentDomain,
			TTLSeconds: cacheTTLSec,
			InsertTime: time.Now().UTC(),
		}
	}

	return currentDomain, nil
}

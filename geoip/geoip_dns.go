package geoip

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"
)

type DnsCacheEntry struct {
	Ips        []netip.Addr
	InsertTime time.Time
	TTLSec     int
}

type DnsResolver struct {
	cache sync.Map
	ctx   context.Context
}

// StartCacheCleaner starts DNS cache periodic cleanup that runs every 10 sec
func (d *DnsResolver) StartCacheCleaner() {
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {
			d.cache.Range(func(key, value any) bool {
				entry := value.(DnsCacheEntry)
				if time.Since(entry.InsertTime).Seconds() > float64(entry.TTLSec) {
					d.cache.Delete(key)
				}
				return true
			})
		}
	}()

}

// Resolve checks cache if hit returns ip if not resolves dns query A record for IPv4 and caches it
func (d *DnsResolver) Resolve(hostname string, cacheTTLSec int) (*netip.Addr, error) {
	if cacheTTLSec < 10 {
		return nil, fmt.Errorf("ttl can not be smaller then 10: %d", cacheTTLSec)
	}

	if cached, exists := d.cache.Load(hostname); exists {
		return &cached.(DnsCacheEntry).Ips[0], nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hostIps, err := net.DefaultResolver.LookupIP(ctx, "ip4", hostname)
	if err != nil {
		return nil, err
	}

	if len(hostIps) == 0 {
		return nil, fmt.Errorf("no IPs found for: %s", hostname)
	}

	ips := make([]netip.Addr, 0, len(hostIps))
	for _, ip := range hostIps {
		hostNetIp, ok := netip.AddrFromSlice(ip)
		if !ok {
			return nil, fmt.Errorf("failed to convert net.IP to netip.Addr: %s", hostIps[0].String())
		}

		ips = append(ips, hostNetIp)
	}

	d.cache.Store(
		hostname,
		DnsCacheEntry{
			Ips:        ips,
			InsertTime: time.Now().UTC(),
			TTLSec:     cacheTTLSec,
		},
	)

	return &ips[0], nil
}

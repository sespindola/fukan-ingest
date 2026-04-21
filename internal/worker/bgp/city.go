package bgp

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"github.com/oschwald/geoip2-golang"
)

// CityLookup wraps a MaxMind GeoLite2-City MMDB for prefix → (lat, lon)
// resolution. A single instance is safe for concurrent reads across the
// BGP parser's goroutines. The MMDB is memory-mapped; Close unmaps it.
type CityLookup struct {
	db *geoip2.Reader
}

// NewCityLookup opens the MMDB at path. Fail-fast semantics: an empty path,
// missing file, or corrupt DB causes the worker to abort at startup rather
// than silently run without enrichment.
func NewCityLookup(path string) (*CityLookup, error) {
	if path == "" {
		return nil, errors.New("geoip: city_db path not configured")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("geoip: city_db stat %q: %w", path, err)
	}
	db, err := geoip2.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: open %q: %w", path, err)
	}
	return &CityLookup{db: db}, nil
}

func (c *CityLookup) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

// BuildEpoch returns the MMDB's build timestamp for startup logging.
func (c *CityLookup) BuildEpoch() uint {
	return c.db.Metadata().BuildEpoch
}

// LookupPrefix parses prefix as CIDR, extracts the network address, and
// returns its (lat, lon). It returns ok=false for parse errors, unresolved
// records, or null-island zero coords — the caller should fall back to
// the ASN-centroid Geo.
func (c *CityLookup) LookupPrefix(prefix string) (float64, float64, bool) {
	if c == nil || c.db == nil {
		return 0, 0, false
	}
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return 0, 0, false
	}
	addr := p.Masked().Addr()
	ip := net.IP(addr.AsSlice())
	rec, err := c.db.City(ip)
	if err != nil {
		return 0, 0, false
	}
	lat := rec.Location.Latitude
	lon := rec.Location.Longitude
	if lat == 0 && lon == 0 {
		return 0, 0, false
	}
	return lat, lon, true
}

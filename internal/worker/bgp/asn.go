package bgp

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"github.com/oschwald/geoip2-golang"
)

// ASNLookup wraps a MaxMind GeoLite2-ASN MMDB for prefix → (holder ASN,
// organization name) resolution. Parallel to CityLookup: same library,
// same delivery pattern, separate MMDB file. A single instance is safe
// for concurrent reads.
//
// Note the semantic: MaxMind returns the AS that *registered* the IP
// block, not necessarily the AS that is currently announcing it. On
// hijacks, the returned ASN + org identifies the legitimate prefix
// holder — which is a feature, not a bug, for the detail-panel UX.
type ASNLookup struct {
	db *geoip2.Reader
}

// NewASNLookup opens the MMDB at path. Fail-fast semantics: empty path,
// missing file, or corrupt DB aborts worker startup rather than running
// with silently-degraded org enrichment.
func NewASNLookup(path string) (*ASNLookup, error) {
	if path == "" {
		return nil, errors.New("geoip: asn_db path not configured")
	}
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("geoip: asn_db stat %q: %w", path, err)
	}
	db, err := geoip2.Open(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: open %q: %w", path, err)
	}
	return &ASNLookup{db: db}, nil
}

func (a *ASNLookup) Close() error {
	if a == nil || a.db == nil {
		return nil
	}
	return a.db.Close()
}

// BuildEpoch returns the MMDB's build timestamp for startup logging.
func (a *ASNLookup) BuildEpoch() uint {
	return a.db.Metadata().BuildEpoch
}

// LookupPrefix parses prefix as CIDR, extracts the network address, and
// returns (ASN, org, ok). A miss is returned for parse errors, unresolved
// records, a zero ASN, or an empty org — any of those leaves the event's
// PrefixAS/PrefixOrg fields at their zero values without dropping it.
func (a *ASNLookup) LookupPrefix(prefix string) (uint32, string, bool) {
	if a == nil || a.db == nil {
		return 0, "", false
	}
	p, err := netip.ParsePrefix(prefix)
	if err != nil {
		return 0, "", false
	}
	addr := p.Masked().Addr()
	ip := net.IP(addr.AsSlice())
	rec, err := a.db.ASN(ip)
	if err != nil {
		return 0, "", false
	}
	if rec.AutonomousSystemNumber == 0 || rec.AutonomousSystemOrganization == "" {
		return 0, "", false
	}
	return uint32(rec.AutonomousSystemNumber), rec.AutonomousSystemOrganization, true
}

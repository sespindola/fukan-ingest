package bgp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Geo resolves an ASN to an approximate (country, lat, lon) by combining a
// static seed table of well-known ASes with an on-demand RIPEstat lookup.
// Unknown ASNs become known the second time they are seen: the first lookup
// is fired asynchronously and the current event is dropped.
type Geo struct {
	mu    sync.RWMutex
	cache map[uint32]geoEntry
	inflight map[uint32]struct{}
	http  *http.Client
}

type geoEntry struct {
	country string
	lat     float64
	lon     float64
}

func NewGeo() *Geo {
	g := &Geo{
		cache:    make(map[uint32]geoEntry, 4096),
		inflight: make(map[uint32]struct{}),
		http:     &http.Client{Timeout: 5 * time.Second},
	}
	for asn, cc := range seedASN {
		if coord, ok := countryCentroids[cc]; ok {
			g.cache[asn] = geoEntry{country: cc, lat: coord.lat, lon: coord.lon}
		}
	}
	return g
}

// Lookup returns (country, lat, lon, ok). On cache miss, a background
// RIPEstat fetch is kicked off and ok=false is returned; subsequent calls
// for the same ASN will hit the cache once the fetch completes.
func (g *Geo) Lookup(asn uint32) (string, float64, float64, bool) {
	g.mu.RLock()
	e, ok := g.cache[asn]
	g.mu.RUnlock()
	if ok {
		return e.country, e.lat, e.lon, true
	}

	g.mu.Lock()
	if _, pending := g.inflight[asn]; !pending {
		g.inflight[asn] = struct{}{}
		go g.fetch(asn)
	}
	g.mu.Unlock()
	return "", 0, 0, false
}

// fetch queries RIPEstat for the country associated with an ASN and stores
// the corresponding country centroid in the cache. Failures are silent: the
// ASN stays unknown and will be retried on the next Lookup after cleanup.
func (g *Geo) fetch(asn uint32) {
	defer func() {
		g.mu.Lock()
		delete(g.inflight, asn)
		g.mu.Unlock()
	}()

	url := fmt.Sprintf("https://stat.ripe.net/data/rir-stats-country/data.json?resource=AS%d", asn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return
	}

	var payload struct {
		Data struct {
			Located []struct {
				Resource string `json:"resource"`
				Location string `json:"location"`
			} `json:"located_resources"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return
	}
	if len(payload.Data.Located) == 0 {
		return
	}
	cc := payload.Data.Located[0].Location
	coord, ok := countryCentroids[cc]
	if !ok {
		return
	}

	g.mu.Lock()
	g.cache[asn] = geoEntry{country: cc, lat: coord.lat, lon: coord.lon}
	g.mu.Unlock()
}

// ParseASN parses a decimal or "ASxxxx" form ASN.
func ParseASN(s string) (uint32, bool) {
	if len(s) > 2 && (s[0] == 'A' || s[0] == 'a') && (s[1] == 'S' || s[1] == 's') {
		s = s[2:]
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

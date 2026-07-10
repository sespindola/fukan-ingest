package cables

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultOverpassURL = "https://overpass-api.de/api/interpreter"

const cableOverpassQuery = `[out:json][timeout:180];
(
  way["communication"="line"]["submarine"="yes"];
  way["communication"="line"]["location"="underwater"];
  way["communication"="line"]["seamark:type"="cable_submarine"];
  way["communication"="line"]["seamark:cable_submarine:category"~"^(telephone|fibre_optic)$"];
);
out body meta;
>;
out skel qt;`

// OSMCable is a single OSM way parsed as a submarine cable (pre-merge).
type OSMCable struct {
	OSMID           uint64
	Name            string
	Ref             string
	Operator        string
	Medium          string
	Category        string
	Coords          []int32 // alternating scaled lat/lon
	BBoxMinLat      int32
	BBoxMinLon      int32
	BBoxMaxLat      int32
	BBoxMaxLon      int32
	SourceUpdatedAt time.Time
}

type overpassResponse struct {
	Elements []overpassElement `json:"elements"`
}

type overpassElement struct {
	Type      string            `json:"type"`
	ID        uint64            `json:"id"`
	Lat       float64           `json:"lat"`
	Lon       float64           `json:"lon"`
	Nodes     []uint64          `json:"nodes"`
	Tags      map[string]string `json:"tags"`
	Timestamp string            `json:"timestamp"`
}

// FetchOSMCables queries Overpass for submarine telecom ways and returns the
// parsed slice plus the count of elements that were skipped (non-matching tags).
func FetchOSMCables(ctx context.Context, endpoint string) ([]OSMCable, int, error) {
	if endpoint == "" {
		endpoint = DefaultOverpassURL
	}
	body, err := downloadOverpass(ctx, endpoint, cableOverpassQuery)
	if err != nil {
		return nil, 0, err
	}
	return ParseOverpassBody(body)
}

func downloadOverpass(ctx context.Context, endpoint, query string) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	form := url.Values{"data": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create overpass request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "fukan-ingest/1.0 (+https://github.com/sespindola/fukan)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download overpass: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("overpass returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return io.ReadAll(resp.Body)
}

// ParseOverpassBody parses the Overpass JSON body into OSMCable records.
func ParseOverpassBody(body []byte) ([]OSMCable, int, error) {
	var resp overpassResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, 0, fmt.Errorf("parse overpass JSON: %w", err)
	}
	nodes := make(map[uint64][2]float64, len(resp.Elements))
	for _, el := range resp.Elements {
		if el.Type == "node" {
			nodes[el.ID] = [2]float64{el.Lat, el.Lon}
		}
	}
	var cables []OSMCable
	var skipped int
	for _, el := range resp.Elements {
		if el.Type != "way" {
			continue
		}
		c, ok := parseOSMWay(el, nodes)
		if !ok {
			skipped++
			continue
		}
		cables = append(cables, c)
	}
	return cables, skipped, nil
}

func parseOSMWay(el overpassElement, nodes map[uint64][2]float64) (OSMCable, bool) {
	if !isTelecomSubmarineCable(el.Tags) {
		return OSMCable{}, false
	}
	coords := make([]int32, 0, len(el.Nodes)*2)
	minLat, minLon := int32(math.MaxInt32), int32(math.MaxInt32)
	maxLat, maxLon := int32(math.MinInt32), int32(math.MinInt32)
	for _, nodeID := range el.Nodes {
		ll, ok := nodes[nodeID]
		if !ok {
			continue
		}
		lat := ScaleCoord(ll[0])
		lon := ScaleCoord(ll[1])
		coords = append(coords, lat, lon)
		if lat < minLat {
			minLat = lat
		}
		if lon < minLon {
			minLon = lon
		}
		if lat > maxLat {
			maxLat = lat
		}
		if lon > maxLon {
			maxLon = lon
		}
	}
	if len(coords) < 4 {
		return OSMCable{}, false
	}
	sourceUpdatedAt := time.Time{}
	if el.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, el.Timestamp); err == nil {
			sourceUpdatedAt = t
		}
	}
	if sourceUpdatedAt.IsZero() {
		sourceUpdatedAt = time.Unix(0, 0).UTC()
	}
	return OSMCable{
		OSMID:           el.ID,
		Name:            tagTrim(el.Tags, "name"),
		Ref:             tagTrim(el.Tags, "ref"),
		Operator:        tagTrim(el.Tags, "operator"),
		Medium:          firstNonEmpty(tagTrim(el.Tags, "telecom:medium"), tagTrim(el.Tags, "seamark:cable_submarine:category")),
		Category:        categoryFor(el.Tags),
		Coords:          coords,
		BBoxMinLat:      minLat,
		BBoxMinLon:      minLon,
		BBoxMaxLat:      maxLat,
		BBoxMaxLon:      maxLon,
		SourceUpdatedAt: sourceUpdatedAt,
	}, true
}

func isTelecomSubmarineCable(tags map[string]string) bool {
	if tags["communication"] != "line" {
		return false
	}
	return tags["submarine"] == "yes" ||
		tags["location"] == "underwater" ||
		tags["seamark:type"] == "cable_submarine" ||
		tags["seamark:cable_submarine:category"] == "telephone" ||
		tags["seamark:cable_submarine:category"] == "fibre_optic"
}

func categoryFor(tags map[string]string) string {
	if c := tagTrim(tags, "seamark:cable_submarine:category"); c != "" {
		return c
	}
	if medium := tagTrim(tags, "telecom:medium"); medium != "" {
		return medium
	}
	return "telecom"
}

func tagTrim(tags map[string]string, key string) string {
	return strings.TrimSpace(tags[key])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

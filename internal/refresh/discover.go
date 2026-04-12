package refresh

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	openSkyBucketURL = "https://s3.opensky-network.org/data-samples"
	metadataPrefix   = "metadata/"
	// Files smaller than this are not aircraft databases (e.g. doc8643, README).
	minAircraftCSVSize = 50_000_000 // 50 MB
)

// s3ListResult represents the S3 ListObjectsV2 XML response.
type s3ListResult struct {
	XMLName  xml.Name   `xml:"ListBucketResult"`
	Contents []s3Object `xml:"Contents"`
}

type s3Object struct {
	Key          string `xml:"Key"`
	Size         int64  `xml:"Size"`
	LastModified string `xml:"LastModified"`
}

// DiscoverLatestAircraftCSV queries the OpenSky S3 bucket and returns the URL
// of the most recently modified aircraft database CSV file.
func DiscoverLatestAircraftCSV(ctx context.Context) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	url := openSkyBucketURL + "?list-type=2&prefix=" + metadataPrefix

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("list bucket: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("bucket list returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read bucket list: %w", err)
	}

	var result s3ListResult
	if err := xml.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("parse bucket XML: %w", err)
	}

	var best s3Object
	var bestTime time.Time

	for _, obj := range result.Contents {
		if !isAircraftCSV(obj.Key) {
			continue
		}
		if obj.Size < minAircraftCSVSize {
			continue
		}

		t, err := time.Parse(time.RFC3339, obj.LastModified)
		if err != nil {
			continue
		}
		if t.After(bestTime) {
			best = obj
			bestTime = t
		}
	}

	if best.Key == "" {
		return "", fmt.Errorf("no aircraft database CSV found in bucket")
	}

	csvURL := openSkyBucketURL + "/" + best.Key
	slog.Info("discovered latest aircraft database CSV",
		"file", best.Key,
		"size_mb", best.Size/(1024*1024),
		"modified", bestTime.Format("2006-01-02"),
	)
	return csvURL, nil
}

// isAircraftCSV checks if a key looks like an aircraft database CSV.
func isAircraftCSV(key string) bool {
	lower := strings.ToLower(key)
	if !strings.HasSuffix(lower, ".csv") {
		return false
	}
	// Match both naming conventions:
	// metadata/aircraft-database-complete-YYYY-MM.csv
	// metadata/aircraftDatabase-YYYY-MM.csv
	// metadata/aircraftDatabase.csv
	return strings.Contains(lower, "aircraft")
}

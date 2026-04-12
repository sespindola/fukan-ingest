package tle

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/akhenakh/sgp4"
)

// FetchCelesTrak downloads GP JSON from CelesTrak and returns parsed OMMs.
func FetchCelesTrak(ctx context.Context, url string) ([]sgp4.OMM, error) {
	body, err := httpGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("celestrak fetch: %w", err)
	}

	omms, err := sgp4.ParseOMMs(body)
	if err != nil {
		return nil, fmt.Errorf("parse OMMs: %w", err)
	}
	return omms, nil
}

// FetchClassified downloads community classified TLEs (3-line format) and
// returns parsed TLEs with names.
func FetchClassified(ctx context.Context, url string) ([]*sgp4.TLE, error) {
	body, err := httpGet(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("classified fetch: %w", err)
	}

	return parseTLEBatch(string(body))
}

// parseTLEBatch parses a multi-TLE string (3-line format: name, line1, line2).
func parseTLEBatch(data string) ([]*sgp4.TLE, error) {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	var tles []*sgp4.TLE

	for i := 0; i+2 < len(lines); {
		line0 := strings.TrimRight(lines[i], "\r")
		line1 := strings.TrimRight(lines[i+1], "\r")
		line2 := strings.TrimRight(lines[i+2], "\r")

		// Detect whether this is 3-line (name + L1 + L2) or 2-line (L1 + L2).
		if len(line0) > 0 && line0[0] != '1' {
			// 3-line format: name on line0
			input := line0 + "\n" + line1 + "\n" + line2
			tle, err := sgp4.ParseTLE(input)
			if err != nil {
				i += 3
				continue
			}
			tles = append(tles, tle)
			i += 3
		} else {
			// 2-line format
			input := line0 + "\n" + line1
			tle, err := sgp4.ParseTLE(input)
			if err != nil {
				i += 2
				continue
			}
			tles = append(tles, tle)
			i += 2
		}
	}
	return tles, nil
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("rate limited (403) — data has not updated since last fetch")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

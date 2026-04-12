package tle

import (
	"context"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/akhenakh/sgp4"
	"github.com/nats-io/nats.go"
	"github.com/sespindola/fukan-ingest/internal/coord"
	"github.com/sespindola/fukan-ingest/internal/model"
	fukanNats "github.com/sespindola/fukan-ingest/internal/nats"
)

type Option func(*TLEWorker)

func WithClassifiedURL(url string) Option {
	return func(w *TLEWorker) { w.classifiedURL = url }
}

const (
	// Maneuver detection thresholds.
	maneuverMeanMotionDelta = 0.01  // rev/day — significant orbit change
	maneuverInclinationDelta = 0.1  // degrees — plane change maneuver

	// Decay detection thresholds.
	decayPerigeeKm = 150.0
	decayBstar     = 0.01 // high drag term
)

// satEntry holds a parsed TLE and its derived metadata.
type satEntry struct {
	tle        *sgp4.TLE
	name       string
	noradID    int
	regime     string
	confidence string
	status     string // "maneuvering", "decaying", or ""
	epoch      time.Time
	params     Params
}

// TLEWorker fetches TLEs from CelesTrak and community sources, then continuously
// propagates satellite positions via SGP4 and publishes to NATS.
type TLEWorker struct {
	nc            *nats.Conn
	source        string
	celestrakURL  string
	classifiedURL string
	fetchInterval time.Duration

	mu    sync.RWMutex
	store map[int]*satEntry // NORAD ID → entry
}

func New(celestrakURL, source string, nc *nats.Conn, opts ...Option) *TLEWorker {
	w := &TLEWorker{
		nc:            nc,
		source:        source,
		celestrakURL:  celestrakURL,
		fetchInterval: 2 * time.Hour,
		store:         make(map[int]*satEntry),
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

func (w *TLEWorker) Run(ctx context.Context) error {
	// Initial fetch with retry — CelesTrak returns 403 if data hasn't updated
	// since the last fetch within the 2-hour cycle.
	retryInterval := 5 * time.Minute
	for {
		err := w.fetchAll(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		slog.Warn("initial TLE fetch failed, retrying",
			"worker", w.Name(), "err", err, "retry_in", retryInterval)
		select {
		case <-time.After(retryInterval):
			retryInterval = min(retryInterval*2, w.fetchInterval)
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	slog.Info("TLE store populated", "worker", w.Name(), "satellites", len(w.store))

	// Start propagation goroutines.
	var wg sync.WaitGroup
	regimes := []struct {
		name     string
		interval time.Duration
	}{
		{RegimeLEO, 30 * time.Second},
		{RegimeMEO, 60 * time.Second},
		{RegimeGEO, 120 * time.Second},
		{RegimeHEO, 30 * time.Second},
	}

	for _, r := range regimes {
		wg.Add(1)
		go func(regime string, interval time.Duration) {
			defer wg.Done()
			w.propagateLoop(ctx, regime, interval)
		}(r.name, r.interval)
	}

	// Periodic re-fetch.
	fetchTicker := time.NewTicker(w.fetchInterval)
	defer fetchTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case <-fetchTicker.C:
			if err := w.fetchAll(ctx); err != nil {
				slog.Warn("TLE re-fetch failed", "worker", w.Name(), "err", err)
			} else {
				slog.Info("TLE store refreshed", "worker", w.Name(), "satellites", len(w.store))
			}
		}
	}
}

func (w *TLEWorker) Name() string {
	return "tle:" + w.source
}

func (w *TLEWorker) fetchAll(ctx context.Context) error {
	omms, err := FetchCelesTrak(ctx, w.celestrakURL)
	if err != nil {
		return err
	}

	newStore := make(map[int]*satEntry, len(omms))

	for _, omm := range omms {
		tle, err := omm.ToTLE()
		if err != nil {
			continue
		}

		params := OrbitalParams(tle)
		regime := ClassifyRegime(tle)

		newStore[omm.NoradCatID] = &satEntry{
			tle:        tle,
			name:       omm.ObjectName,
			noradID:    omm.NoradCatID,
			regime:     regime,
			confidence: "official",
			epoch:      tle.EpochTime(),
			params:     params,
		}
	}

	// Fetch classified TLEs if configured.
	if w.classifiedURL != "" {
		tles, err := FetchClassified(ctx, w.classifiedURL)
		if err != nil {
			slog.Warn("classified TLE fetch failed", "err", err)
		} else {
			for _, t := range tles {
				params := OrbitalParams(t)
				regime := ClassifyRegime(t)

				newStore[t.SatelliteNumber] = &satEntry{
					tle:        t,
					name:       t.Name,
					noradID:    t.SatelliteNumber,
					regime:     regime,
					confidence: "community_derived",
					epoch:      t.EpochTime(),
					params:     params,
				}
			}
			slog.Info("classified TLEs loaded", "count", len(tles))
		}
	}

	// Detect maneuvers by comparing against the previous store.
	w.mu.RLock()
	oldStore := w.store
	w.mu.RUnlock()

	var maneuvers int
	for id, entry := range newStore {
		old, exists := oldStore[id]
		if !exists {
			continue
		}
		if detectManeuver(old.params, entry.params) {
			entry.status = "maneuvering"
			maneuvers++
		}
	}
	if maneuvers > 0 {
		slog.Info("maneuvers detected", "count", maneuvers)
	}

	w.mu.Lock()
	w.store = newStore
	w.mu.Unlock()

	return nil
}

func (w *TLEWorker) propagateLoop(ctx context.Context, regime string, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var count int64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n := w.propagateRegime(ctx, regime)
			count += int64(n)
			if count > 0 && count%(10000) < int64(n) {
				slog.Info("satellite positions propagated",
					"worker", w.Name(), "regime", regime, "total", count)
			}
		}
	}
}

func (w *TLEWorker) propagateRegime(ctx context.Context, regime string) int {
	now := time.Now().UTC()

	w.mu.RLock()
	entries := make([]*satEntry, 0, len(w.store)/4)
	for _, e := range w.store {
		if e.regime == regime {
			entries = append(entries, e)
		}
	}
	w.mu.RUnlock()

	var published int
	for _, e := range entries {
		if ctx.Err() != nil {
			break
		}

		event, ok := propagateOne(e, now)
		if !ok {
			continue
		}

		if err := fukanNats.PublishJSON(w.nc, "fukan.telemetry.satellite", event); err != nil {
			slog.Warn("nats publish failed", "asset_id", event.AssetID, "err", err)
		}
		published++
	}
	return published
}

func propagateOne(e *satEntry, now time.Time) (model.FukanEvent, bool) {
	eci, err := e.tle.FindPositionAtTime(now)
	if err != nil {
		return model.FukanEvent{}, false
	}

	lat, lon, alt := eci.ToGeodetic()

	h3cell, err := coord.ComputeH3(lat, lon)
	if err != nil {
		return model.FukanEvent{}, false
	}

	confidence := e.confidence
	staleDays := now.Sub(e.epoch).Hours() / 24
	switch e.regime {
	case RegimeLEO, RegimeHEO:
		if staleDays > 14 {
			confidence = "stale"
		}
	case RegimeMEO, RegimeGEO:
		if staleDays > 30 {
			confidence = "stale"
		}
	}

	source := "celestrak"
	if e.confidence == "community_derived" {
		source = "classfd"
	}

	// Determine satellite status: maneuver detection is set during fetch,
	// decay detection is checked at propagation time.
	status := e.status
	if status == "" && detectDecay(e) {
		status = "decaying"
	}

	event := model.FukanEvent{
		Timestamp:     now.UnixMilli(),
		AssetID:       strconv.Itoa(e.noradID),
		AssetType:     model.AssetSatellite,
		Callsign:      e.name,
		Lat:           coord.ScaleLat(lat),
		Lon:           coord.ScaleLon(lon),
		Alt:           int32(alt * 1000), // km → meters
		H3Cell:        h3cell,
		Source:        source,
		OrbitRegime:   e.regime,
		Confidence:    confidence,
		TLEEpoch:      e.epoch.UnixMilli(),
		Inclination:   float32(e.params.Inclination),
		PeriodMinutes: float32(e.params.PeriodMinutes),
		ApogeeKm:      float32(e.params.ApogeeKm),
		PerigeeKm:     float32(e.params.PerigeeKm),
		SatStatus:     status,
	}

	if err := model.Validate(event); err != nil {
		return model.FukanEvent{}, false
	}
	return event, true
}

// detectManeuver compares old and new orbital parameters to detect orbit changes.
func detectManeuver(old, new Params) bool {
	if math.Abs(new.PeriodMinutes-old.PeriodMinutes)*old.PeriodMinutes > 0 &&
		math.Abs(1440.0/new.PeriodMinutes-1440.0/old.PeriodMinutes) > maneuverMeanMotionDelta {
		return true
	}
	if math.Abs(new.Inclination-old.Inclination) > maneuverInclinationDelta {
		return true
	}
	return false
}

// detectDecay checks if a satellite's orbit indicates imminent reentry.
func detectDecay(e *satEntry) bool {
	return e.params.PerigeeKm < decayPerigeeKm || e.tle.Bstar > decayBstar
}

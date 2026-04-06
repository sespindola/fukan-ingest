package refresh

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/proto"
)

const batchSize = 50_000

// AircraftMeta represents a single row from the OpenSky aircraft database CSV,
// optionally enriched with image data from Planespotters (populated by Rails).
type AircraftMeta struct {
	ICAO24           string
	Registration     string
	ManufacturerName string
	Model            string
	Typecode         string
	ICAOAircraftType string
	Operator         string
	OperatorCallsign string
	OperatorICAO     string
	OperatorIATA     string
	Owner            string
	Built            string
	Status           string
	CategoryDesc     string
	ImageURL         string
	ImageAttribution string
}

// columnMap maps CSV header names to AircraftMeta fields.
var columnMap = map[string]func(*AircraftMeta, string){
	"icao24":            func(m *AircraftMeta, v string) { m.ICAO24 = strings.ToUpper(strings.TrimSpace(v)) },
	"registration":      func(m *AircraftMeta, v string) { m.Registration = strings.TrimSpace(v) },
	"manufacturername":  func(m *AircraftMeta, v string) { m.ManufacturerName = strings.TrimSpace(v) },
	"model":             func(m *AircraftMeta, v string) { m.Model = strings.TrimSpace(v) },
	"typecode":          func(m *AircraftMeta, v string) { m.Typecode = strings.TrimSpace(v) },
	"icaoaircraftclass": func(m *AircraftMeta, v string) { m.ICAOAircraftType = strings.TrimSpace(v) },
	"operator":          func(m *AircraftMeta, v string) { m.Operator = strings.TrimSpace(v) },
	"operatorcallsign":  func(m *AircraftMeta, v string) { m.OperatorCallsign = strings.TrimSpace(v) },
	"operatoricao":      func(m *AircraftMeta, v string) { m.OperatorICAO = strings.TrimSpace(v) },
	"operatoriata":      func(m *AircraftMeta, v string) { m.OperatorIATA = strings.TrimSpace(v) },
	"owner":             func(m *AircraftMeta, v string) { m.Owner = strings.TrimSpace(v) },
	"built":             func(m *AircraftMeta, v string) { m.Built = strings.TrimSpace(v) },
	"status":            func(m *AircraftMeta, v string) { m.Status = strings.TrimSpace(v) },
	"categorydescription": func(m *AircraftMeta, v string) { m.CategoryDesc = strings.TrimSpace(v) },
}

// Aircraft downloads the OpenSky aircraft database CSV and loads it into ClickHouse.
// If token is non-empty, it is sent as a Bearer token. If dryRun is true, rows are
// parsed and counted but not inserted.
func Aircraft(ctx context.Context, conn *ch.Client, csvURL, token string, dryRun bool) error {
	start := time.Now()

	slog.Info("downloading aircraft database", "url", csvURL)
	body, err := downloadCSV(ctx, csvURL, token)
	if err != nil {
		return err
	}
	defer body.Close()

	reader := csv.NewReader(&singleToDoubleQuoteReader{r: body})
	reader.ReuseRecord = true

	// Read header and build index mapping.
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read csv header: %w", err)
	}
	indices := buildHeaderIndex(header)

	// Step 1: Parse entire CSV into memory so we can enrich before inserting.
	allAircraft := make(map[string]*AircraftMeta, 650_000)
	var skipped int

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			skipped++
			continue
		}

		meta := parseRecord(record, indices)
		if meta.ICAO24 == "" {
			skipped++
			continue
		}
		allAircraft[meta.ICAO24] = &meta

		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	slog.Info("csv parsing complete", "aircraft", len(allAircraft), "skipped", skipped)

	// Step 2: Carry forward existing image data so a refresh doesn't wipe it.
	if !dryRun && conn != nil {
		preserved, err := queryExistingImages(ctx, conn)
		if err != nil {
			return fmt.Errorf("query existing images: %w", err)
		}
		for icao24, img := range preserved {
			if meta, ok := allAircraft[icao24]; ok {
				meta.ImageURL = img.ImageURL
				meta.ImageAttribution = img.ImageAttribution
			}
		}
		if len(preserved) > 0 {
			slog.Info("preserved existing image data", "count", len(preserved))
		}
	}

	// Step 3: Batch-insert all rows.
	batch := make([]AircraftMeta, 0, batchSize)
	var flushed int

	for _, meta := range allAircraft {
		batch = append(batch, *meta)

		if len(batch) >= batchSize {
			if !dryRun {
				if err := insertAircraftBatch(ctx, conn, batch); err != nil {
					return fmt.Errorf("insert batch: %w", err)
				}
			}
			flushed += len(batch)
			batch = batch[:0]
		}

		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	if len(batch) > 0 {
		if !dryRun {
			if err := insertAircraftBatch(ctx, conn, batch); err != nil {
				return fmt.Errorf("insert final batch: %w", err)
			}
		}
		flushed += len(batch)
	}

	action := "inserted"
	if dryRun {
		action = "would insert (dry-run)"
	}

	slog.Info("aircraft metadata refresh complete",
		"action", action,
		"parsed", len(allAircraft),
		"skipped", skipped,
		"rows", flushed,
		"duration", time.Since(start).Round(time.Millisecond),
	)
	return nil
}

// queryExistingImages returns image data for aircraft that already have images in ClickHouse.
func queryExistingImages(ctx context.Context, conn *ch.Client) (map[string]struct{ ImageURL, ImageAttribution string }, error) {
	var (
		colICAO24 proto.ColStr
		colURL    proto.ColStr
		colAttr   proto.ColStr
	)

	result := make(map[string]struct{ ImageURL, ImageAttribution string })

	err := conn.Do(ctx, ch.Query{
		Body: "SELECT icao24, image_url, image_attribution FROM fukan.aircraft_meta WHERE image_url != ''",
		Result: proto.Results{
			{Name: "icao24", Data: &colICAO24},
			{Name: "image_url", Data: &colURL},
			{Name: "image_attribution", Data: &colAttr},
		},
		OnResult: func(ctx context.Context, block proto.Block) error {
			for i := 0; i < block.Rows; i++ {
				result[colICAO24.Row(i)] = struct{ ImageURL, ImageAttribution string }{
					ImageURL:         colURL.Row(i),
					ImageAttribution: colAttr.Row(i),
				}
			}
			colICAO24.Reset()
			colURL.Reset()
			colAttr.Reset()
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func downloadCSV(ctx context.Context, csvURL, token string) (io.ReadCloser, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, csvURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download csv: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("csv download returned %d — OpenSky OAuth2 credentials may be required (set OPENSKY_CLIENT_ID and OPENSKY_CLIENT_SECRET)", resp.StatusCode)
		}
		return nil, fmt.Errorf("csv download returned status %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// singleToDoubleQuoteReader translates single quotes to double quotes on the fly
// so that Go's encoding/csv (which only understands double-quote delimiters) can
// parse OpenSky's single-quoted CSV format natively.
type singleToDoubleQuoteReader struct {
	r io.Reader
}

func (q *singleToDoubleQuoteReader) Read(p []byte) (int, error) {
	n, err := q.r.Read(p)
	for i := 0; i < n; i++ {
		if p[i] == '\'' {
			p[i] = '"'
		}
	}
	return n, err
}

// buildHeaderIndex maps lowercase column names to their CSV column indices.
func buildHeaderIndex(header []string) map[int]func(*AircraftMeta, string) {
	idx := make(map[int]func(*AircraftMeta, string))
	for i, col := range header {
		col = strings.ToLower(strings.TrimSpace(col))
		if setter, ok := columnMap[col]; ok {
			idx[i] = setter
		}
	}
	return idx
}

func parseRecord(record []string, indices map[int]func(*AircraftMeta, string)) AircraftMeta {
	var meta AircraftMeta
	for i, setter := range indices {
		if i < len(record) {
			setter(&meta, record[i])
		}
	}
	return meta
}

func insertAircraftBatch(ctx context.Context, conn *ch.Client, batch []AircraftMeta) error {
	var (
		colICAO24        proto.ColStr
		colRegistration  proto.ColStr
		colManufName     proto.ColStr
		colModel         proto.ColStr
		colTypecode      proto.ColStr
		colAircraftType  proto.ColStr
		colOperator      proto.ColStr
		colOpCallsign    proto.ColStr
		colOpICAO        proto.ColStr
		colOpIATA        proto.ColStr
		colOwner         proto.ColStr
		colBuilt         proto.ColStr
		colStatus        proto.ColStr
		colCategoryDesc  proto.ColStr
		colImageURL      proto.ColStr
		colImageAttr     proto.ColStr
	)

	for _, r := range batch {
		colICAO24.Append(r.ICAO24)
		colRegistration.Append(r.Registration)
		colManufName.Append(r.ManufacturerName)
		colModel.Append(r.Model)
		colTypecode.Append(r.Typecode)
		colAircraftType.Append(r.ICAOAircraftType)
		colOperator.Append(r.Operator)
		colOpCallsign.Append(r.OperatorCallsign)
		colOpICAO.Append(r.OperatorICAO)
		colOpIATA.Append(r.OperatorIATA)
		colOwner.Append(r.Owner)
		colBuilt.Append(r.Built)
		colStatus.Append(r.Status)
		colCategoryDesc.Append(r.CategoryDesc)
		colImageURL.Append(r.ImageURL)
		colImageAttr.Append(r.ImageAttribution)
	}

	input := proto.Input{
		{Name: "icao24", Data: &colICAO24},
		{Name: "registration", Data: &colRegistration},
		{Name: "manufacturer_name", Data: &colManufName},
		{Name: "model", Data: &colModel},
		{Name: "typecode", Data: &colTypecode},
		{Name: "icao_aircraft_type", Data: &colAircraftType},
		{Name: "operator", Data: &colOperator},
		{Name: "operator_callsign", Data: &colOpCallsign},
		{Name: "operator_icao", Data: &colOpICAO},
		{Name: "operator_iata", Data: &colOpIATA},
		{Name: "owner", Data: &colOwner},
		{Name: "built", Data: &colBuilt},
		{Name: "status", Data: &colStatus},
		{Name: "category_desc", Data: &colCategoryDesc},
		{Name: "image_url", Data: &colImageURL},
		{Name: "image_attribution", Data: &colImageAttr},
	}

	return conn.Do(ctx, ch.Query{
		Body:  "INSERT INTO fukan.aircraft_meta (icao24, registration, manufacturer_name, model, typecode, icao_aircraft_type, operator, operator_callsign, operator_icao, operator_iata, owner, built, status, category_desc, image_url, image_attribution) VALUES",
		Input: input,
	})
}

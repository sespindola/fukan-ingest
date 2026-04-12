package refresh

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/proto"
)

const defaultGCATURL = "https://planet4589.org/space/gcat/tsv/cat/satcat.tsv"

// SatelliteMeta represents a row from the GCAT satellite catalog TSV.
type SatelliteMeta struct {
	NoradCatID  uint32
	JCAT        string
	ObjectName  string
	Owner       string
	State       string
	ObjectType  string
	Purpose     string
	LaunchDate  time.Time
	MassKg      float32
	PerigeeKm   float32
	ApogeeKm    float32
	Inclination float32
	Status      string
}

// Satellite downloads the GCAT satellite catalog TSV and loads into ClickHouse.
func Satellite(ctx context.Context, conn *ch.Client, gcatURL string, dryRun bool) error {
	start := time.Now()

	if gcatURL == "" {
		gcatURL = defaultGCATURL
	}

	slog.Info("downloading GCAT satellite catalog", "url", gcatURL)

	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, gcatURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	// Read header line to find column indices.
	if !scanner.Scan() {
		return fmt.Errorf("empty TSV")
	}
	header := scanner.Text()
	indices := buildSatHeaderIndex(header)

	var (
		batch   []SatelliteMeta
		total   int
		skipped int
	)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		line := scanner.Text()
		if line == "" || line[0] == '#' {
			continue
		}

		meta, ok := parseSatRecord(line, indices)
		if !ok {
			skipped++
			continue
		}

		batch = append(batch, meta)
		total++

		if len(batch) >= batchSize {
			if !dryRun {
				if err := insertSatelliteBatch(ctx, conn, batch); err != nil {
					return fmt.Errorf("insert batch: %w", err)
				}
			}
			batch = batch[:0]
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read tsv: %w", err)
	}

	if len(batch) > 0 && !dryRun {
		if err := insertSatelliteBatch(ctx, conn, batch); err != nil {
			return fmt.Errorf("insert final batch: %w", err)
		}
	}

	action := "inserted"
	if dryRun {
		action = "would insert (dry-run)"
	}

	slog.Info("satellite metadata refresh complete",
		"action", action,
		"rows", total,
		"skipped", skipped,
		"duration", time.Since(start).Round(time.Millisecond),
	)
	return nil
}

// GCAT TSV column names (case-insensitive matching).
var satColumnNames = map[string]string{
	"satcat":  "satcat",
	"#jcat":   "jcat",
	"jcat":    "jcat",
	"name":    "name",
	"plname":  "name",
	"owner":   "owner",
	"state":   "state",
	"type":    "objtype",
	"ldate":   "ldate",
	"mass":    "mass",
	"perigee": "perigee",
	"apogee":  "apogee",
	"inc":     "inc",
	"status":  "status",
}

func buildSatHeaderIndex(header string) map[int]string {
	fields := strings.Split(header, "\t")
	idx := make(map[int]string, len(fields))
	for i, f := range fields {
		key := strings.ToLower(strings.TrimSpace(f))
		if mapped, ok := satColumnNames[key]; ok {
			idx[i] = mapped
		}
	}
	return idx
}

func parseSatRecord(line string, indices map[int]string) (SatelliteMeta, bool) {
	fields := strings.Split(line, "\t")
	var meta SatelliteMeta

	for i, col := range indices {
		if i >= len(fields) {
			continue
		}
		val := strings.TrimSpace(fields[i])
		if val == "" || val == "-" {
			continue
		}

		switch col {
		case "satcat":
			n, err := strconv.ParseUint(val, 10, 32)
			if err != nil {
				continue
			}
			meta.NoradCatID = uint32(n)
		case "jcat":
			meta.JCAT = val
		case "name":
			if meta.ObjectName == "" {
				meta.ObjectName = val
			}
		case "owner":
			meta.Owner = val
		case "state":
			meta.State = val
		case "objtype":
			meta.ObjectType = val
		case "ldate":
			t, err := time.Parse("2006 Jan _2", val)
			if err != nil {
				t, err = time.Parse("2006-01-02", val)
			}
			if err == nil {
				meta.LaunchDate = t
			}
		case "mass":
			f, err := strconv.ParseFloat(val, 32)
			if err == nil {
				meta.MassKg = float32(f)
			}
		case "perigee":
			f, err := strconv.ParseFloat(val, 32)
			if err == nil {
				meta.PerigeeKm = float32(f)
			}
		case "apogee":
			f, err := strconv.ParseFloat(val, 32)
			if err == nil {
				meta.ApogeeKm = float32(f)
			}
		case "inc":
			f, err := strconv.ParseFloat(val, 32)
			if err == nil {
				meta.Inclination = float32(f)
			}
		case "status":
			meta.Status = val
		}
	}

	// Must have at least JCAT or NORAD ID.
	if meta.JCAT == "" && meta.NoradCatID == 0 {
		return SatelliteMeta{}, false
	}

	return meta, true
}

func insertSatelliteBatch(ctx context.Context, conn *ch.Client, batch []SatelliteMeta) error {
	var (
		colNoradCatID proto.ColUInt32
		colJCAT       proto.ColStr
		colObjectName proto.ColStr
		colPurpose    proto.ColStr
		colLaunchDate proto.ColDate
		colMassKg     proto.ColFloat32
		colPerigeeKm  proto.ColFloat32
		colApogeeKm   proto.ColFloat32
		colInclination proto.ColFloat32
	)
	colOwner := proto.NewLowCardinality[string](&proto.ColStr{})
	colState := proto.NewLowCardinality[string](&proto.ColStr{})
	colObjectType := proto.NewLowCardinality[string](&proto.ColStr{})
	colStatus := proto.NewLowCardinality[string](&proto.ColStr{})

	for _, r := range batch {
		colNoradCatID.Append(r.NoradCatID)
		colJCAT.Append(r.JCAT)
		colObjectName.Append(r.ObjectName)
		colOwner.Append(r.Owner)
		colState.Append(r.State)
		colObjectType.Append(r.ObjectType)
		colPurpose.Append(r.Purpose)
		colLaunchDate.Append(r.LaunchDate)
		colMassKg.Append(r.MassKg)
		colPerigeeKm.Append(r.PerigeeKm)
		colApogeeKm.Append(r.ApogeeKm)
		colInclination.Append(r.Inclination)
		colStatus.Append(r.Status)
	}

	input := proto.Input{
		{Name: "norad_cat_id", Data: &colNoradCatID},
		{Name: "jcat", Data: &colJCAT},
		{Name: "object_name", Data: &colObjectName},
		{Name: "owner", Data: colOwner},
		{Name: "state", Data: colState},
		{Name: "object_type", Data: colObjectType},
		{Name: "purpose", Data: &colPurpose},
		{Name: "launch_date", Data: &colLaunchDate},
		{Name: "mass_kg", Data: &colMassKg},
		{Name: "perigee_km", Data: &colPerigeeKm},
		{Name: "apogee_km", Data: &colApogeeKm},
		{Name: "inclination", Data: &colInclination},
		{Name: "status", Data: colStatus},
	}

	return conn.Do(ctx, ch.Query{
		Body:  "INSERT INTO fukan.satellite_meta VALUES",
		Input: input,
	})
}

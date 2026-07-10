package refresh

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/ch-go"
	"github.com/ClickHouse/ch-go/proto"
)

const defaultGCATURL = "https://planet4589.org/space/gcat/tsv/cat/satcat.tsv"
const defaultGCATOrgsURL = "https://planet4589.org/space/gcat/tsv/tables/orgs.tsv"

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

	orgNames, err := fetchGCATOrgNames(ctx, defaultGCATOrgsURL)
	if err != nil {
		return fmt.Errorf("download GCAT organizations: %w", err)
	}
	slog.Info("loaded GCAT organization names", "rows", len(orgNames))

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

		meta, ok := parseSatRecord(line, indices, orgNames)
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

func parseSatRecord(line string, indices map[int]string, orgNames map[string]string) (SatelliteMeta, bool) {
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
			meta.Owner = humanizeOrgCode(val, orgNames)
		case "state":
			meta.State = humanizeOrgCode(val, orgNames)
		case "objtype":
			meta.ObjectType = humanizeSatType(val)
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
			meta.Status = humanizeSatStatus(val)
		}
	}

	// Must have at least JCAT or NORAD ID.
	if meta.JCAT == "" && meta.NoradCatID == 0 {
		return SatelliteMeta{}, false
	}

	return meta, true
}

func fetchGCATOrgNames(ctx context.Context, orgsURL string) (map[string]string, error) {
	client := &http.Client{Timeout: 5 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orgsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	return parseGCATOrgNames(resp.Body)
}

func parseGCATOrgNames(r io.Reader) (map[string]string, error) {
	reader := csv.NewReader(r)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	reader.LazyQuotes = true

	orgNames := make(map[string]string, 4_000)
	for {
		record, err := reader.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("read orgs tsv: %w", err)
		}
		if len(record) < 16 || strings.HasPrefix(record[0], "#") {
			continue
		}

		code := strings.TrimSpace(record[0])
		if code == "" {
			continue
		}

		name := firstNonPlaceholder(record[15], record[8], record[14], record[7])
		if name != "" {
			orgNames[code] = name
		}
	}

	return orgNames, nil
}

func firstNonPlaceholder(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && value != "-" {
			return value
		}
	}
	return ""
}

func humanizeOrgCode(code string, orgNames map[string]string) string {
	if name, ok := orgNames[code]; ok {
		return name
	}
	return code
}

func humanizeSatStatus(status string) string {
	status = strings.Join(strings.Fields(status), " ")
	if status == "" {
		return ""
	}
	if label, ok := satStatusLabels[status]; ok {
		return label
	}
	return status
}

var satStatusLabels = map[string]string{
	"O":      "In orbit",
	"AO":     "Attached in orbit",
	"AO IN":  "Attached inside",
	"UDK":    "Undocked",
	"REL":    "Released",
	"DEP":    "Deployed",
	"TO":     "Takeoff",
	"TOA":    "Attached at takeoff",
	"OX":     "In orbit, probably lost",
	"E":      "Exploded",
	"N":      "Renamed",
	"NA":     "Renamed attached",
	"LEASE":  "Lease",
	"S":      "Targeted suborbital entry/impact",
	"R":      "Reentered",
	"D":      "Deorbited",
	"L":      "Landed",
	"LF":     "Failed landing",
	"AS":     "Suborbital reentered attached",
	"AR":     "Reentered attached",
	"AR IN":  "Reentered inside",
	"AL":     "Landed attached",
	"AL IN":  "Landed inside",
	"TX":     "Transmission ended",
	"F":      "Failed",
	"AF":     "Failed attached",
	"DSO":    "Deep space",
	"DSA":    "Deep space attached",
	"DSA IN": "Deep space inside",
	"LO":     "Liftoff",
	"LOA":    "Reflight attached",
	"DK":     "Docked",
	"GRP":    "Grappled",
	"ATT":    "Attached",
	"TFR":    "Transfer",
	"TFR E":  "Transfer external",
	"TFR IN": "Transfer in",
	"C":      "Collided",
	"EVA DP": "Spacewalk depressurization",
	"EVA RP": "Spacewalk repressurization",
	"REFLT":  "Reflight",
	"EO":     "Escape",
	"EAO":    "Escape attached",
	"EN":     "Encounter",
	"OI":     "Orbit insertion",
	"OE":     "Orbit escape",
	"ERR":    "Error",
}

func humanizeSatType(satType string) string {
	if satType == "" {
		return ""
	}

	var labels []string
	bytes := []byte(satType)
	for i, b := range bytes {
		if b == ' ' || b == '-' || b == 0 {
			continue
		}
		if label := satTypeByteLabel(i, b); label != "" {
			labels = append(labels, label)
		} else {
			labels = append(labels, fmt.Sprintf("byte %d: %c", i+1, b))
		}
	}
	if len(labels) == 0 {
		return ""
	}
	return strings.Join(labels, "; ")
}

func satTypeByteLabel(index int, value byte) string {
	if index == 1 {
		return satTypeModifierLabel(value)
	}
	if index == 7 {
		return satTypeConstellationLabel(value)
	}
	if index == 9 {
		return satTypeAnnotationLabel(value)
	}
	if index < 0 || index >= len(satTypeByteLabels) {
		return ""
	}
	return satTypeByteLabels[index][value]
}

var satTypeByteLabels = []map[byte]string{
	{
		'P': "Payload",
		'C': "Component",
		'R': "Launch vehicle stage",
		'D': "Fragmentation debris",
		'S': "Suborbital payload",
		'X': "Deleted catalog entry",
		'Z': "Spurious catalog entry",
	},
	nil,
	{
		'A': "Permanently attached",
		'F': "Stuck attached",
		'S': "Expected to separate",
		'T': "Transferred without free flight",
		'I': "Internal",
	},
	{
		'A': "Payload adapter/support structure",
		'B': "Battery explosion debris",
		'C': "Calibration/test object or chaff",
		'D': "Dummy satellite",
		'E': "Tethered EVA spacesuit",
		'F': "Fairing or cover",
		'G': "General debris",
		'H': "Human spaceflight related",
		'I': "Collision debris",
		'J': "Anomalous debris",
		'K': "Possible solid motor slag",
		'L': "Separated after landing",
		'M': "Jettisoned motor or tank",
		'N': "Nuclear reactor core or coolant blob",
		'O': "Unknown debris released at orbit insertion",
		'P': "Propulsion-related breakup debris",
		'Q': "Low-perigee aerodynamic breakup debris",
		'R': "Reentry vehicle",
		'S': "Subsatellite or subpayload",
		'T': "Ejected payload section",
		'U': "Untethered EVA suit",
		'V': "Ejection mechanism",
		'W': "Weapons-test debris",
		'X': "Debris of unknown nature",
		'Y': "Despin device",
		'Z': "On-board destruct breakup debris",
	},
	{
		'D': "Deep space or escape trajectory",
		'E': "Destroyed in pad explosion",
		'F': "Failed to reach orbit",
		'L': "Active on planetary surface",
		'M': "Missing from SATCAT by mistake",
		'O': "Orbital-energy non-orbit",
		'P': "Partial orbit",
		'R': "Reentry orbit",
		'S': "Near orbit",
		'T': "Transient orbit",
		'V': "Escape energy, not deep space",
		'X': "Extraterrestrial launch catalog entry",
		'Z': "Extraterrestrial launch recataloged",
	},
	{
		'I': "Station program",
		'C': "Station major non-module component",
		'D': "Station deployable subsatellite",
		'E': "Station EVA equipment",
		'G': "Station generic cargo",
		'M': "Station module",
		'S': "Space Shuttle program",
		'T': "Visiting vehicle piece",
		'U': "Visiting vehicle or deployed rocket stage",
		'V': "Station visiting vehicle",
	},
	{
		'U': "UN registered or registration expected",
		'X': "UN registered but should not be",
	},
	nil,
	{
		'?': "Uncertain identification",
		'+': "Starlink filed out of service, maneuverable",
		'*': "Starlink filed out of service, non-maneuverable",
		'm': "Multiple-object placeholder",
		'C': "Classified orbital data",
		'c': "Previously classified orbital data",
		'U': "ISS/station cargo launch assignment uncertain",
		'D': "ISS/station cargo return assignment uncertain",
		'X': "Unknown launch association",
		's': "TLE/SupTLE disagreement",
	},
	nil,
	{
		'+': "Included in debris cloud analysis",
		'-': "Excluded from debris cloud analysis",
	},
}

func satTypeModifierLabel(value byte) string {
	if value >= '1' && value <= '5' {
		return fmt.Sprintf("Launch vehicle stage %c", value)
	}
	switch value {
	case 'A':
		return "Alias entry"
	case 'H':
		return "Spaceship with humans aboard at launch"
	case 'P':
		return "Pressurized spaceship or module without humans at launch"
	case 'X':
		return "Non-standard payload or suborbital special case"
	case 'C':
		return "Cargo placeholder"
	case 'D':
		return "Separately integrated deployer"
	}
	return ""
}

func satTypeConstellationLabel(value byte) string {
	switch value {
	case '*':
		return "Launch-failure culprit"
	case 'A':
		return "Satellite ascending to operational orbit"
	case 'D':
		return "Satellite in plane drift orbit"
	case 'F':
		return "Satellite failed early in mission"
	case 'G':
		return "Satellite retired to graveyard orbit"
	case 'L':
		return "Satellite removed far from operational constellation"
	case 'M':
		return "Satellite failed in operational orbit and decaying uncontrolled"
	case 'O':
		return "Satellite active in operational orbit"
	case 'R':
		return "Satellite active orbit lowering to reentry"
	case 'S':
		return "Satellite used for special tests"
	case 'T':
		return "Satellite slightly removed from operational constellation"
	case 'U':
		return "Satellite apparently malfunctioning"
	}
	return ""
}

func satTypeAnnotationLabel(value byte) string {
	colors := map[byte]string{
		'r': "red",
		'g': "green",
		'b': "blue",
		'c': "cyan",
		'm': "magenta",
		'y': "yellow",
		'k': "black",
	}
	if color, ok := colors[value]; ok {
		return "Annotation color: " + color
	}
	return ""
}

func insertSatelliteBatch(ctx context.Context, conn *ch.Client, batch []SatelliteMeta) error {
	var (
		colNoradCatID  proto.ColUInt32
		colJCAT        proto.ColStr
		colObjectName  proto.ColStr
		colPurpose     proto.ColStr
		colLaunchDate  proto.ColDate
		colMassKg      proto.ColFloat32
		colPerigeeKm   proto.ColFloat32
		colApogeeKm    proto.ColFloat32
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

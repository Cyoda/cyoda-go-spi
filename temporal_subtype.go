package spi

import "time"

// This file is a faithful port of Cyoda Cloud's temporal search machinery:
//   - per-subtype ISO-8601 parsing (client LeafFieldParser.kt / DataType.kt), and
//   - the polymorphic downscale/upscale resolution graph
//     (tree-node PolymorphicTemporalConversions.kt).
//
// A polymorphic temporal field can have been stored across several of the six
// temporal subtypes (LocalDate, LocalDateTime, LocalTime, ZonedDateTime, Year,
// YearMonth). A single query operand is parsed once to its natural subtype, then
// resolved into a per-declared-type condition. Downscaling (fine -> coarse)
// floors the value every hop and, on an imprecise hop, may mutate a strict/
// inclusive comparison; upscaling (coarse -> fine) floors to the start of the
// period and never touches the operation. Both are reproduced exactly here,
// including the deliberate downscale/upscale asymmetry.
//
// Millis/zone convention: every zone-less subtype (Year, YearMonth, LocalDate,
// LocalDateTime, LocalTime) is anchored to UTC when reduced to epoch-millis —
// its wall-clock fields are read as if the zone were UTC. Only ZonedDateTime
// carries a real instant (its offset is honoured). This matches
// ParseTemporalMillis, which treats an offset-bearing RFC3339 string as an
// absolute instant, and keeps a coarse operand's floored value comparable
// against stored values reduced the same way. When a ZonedDateTime is
// downscaled its zone is dropped (Cloud does no timezone math on that hop): the
// wall-clock fields survive and are thereafter read at UTC.

// TemporalValue is a parsed temporal operand together with the subtype
// (granularity) it currently represents. It is produced by ParseTemporalSubtype
// and threaded through the resolution graph, which floors and re-tags it.
type TemporalValue struct {
	// Type is the subtype this value currently represents.
	Type DataType
	// wall holds the value's wall-clock fields placed at UTC. For every zone-less
	// subtype this is the value itself; for a ZonedDateTime it is the local
	// (offset-zone) wall time, so dropping the zone on downscale is a no-op here.
	wall time.Time
	// offsetSecs is the ZonedDateTime offset east of UTC in seconds. It is
	// meaningful only while zoned is true.
	offsetSecs int
	// zoned is true only for a live ZonedDateTime value (an offset-bearing parse,
	// or one produced by upscaling to ZONED_DATE_TIME at UTC). A downscale to
	// LOCAL_DATE_TIME clears it.
	zoned bool
}

// Millis returns the value as floored epoch-milliseconds, ready to feed
// CompareTemporal. Zone-less subtypes are read at UTC; a live ZonedDateTime
// honours its offset to yield the true instant.
func (tv TemporalValue) Millis() int64 {
	if tv.zoned {
		return tv.wall.Add(-time.Duration(tv.offsetSecs) * time.Second).UnixMilli()
	}
	return tv.wall.UnixMilli()
}

// epochDate is java.time.LocalDate.EPOCH (1970-01-01), the anchor date for
// LocalTime and the precision reference for the LDT->LOCAL_TIME edge.
var epochYear, epochMonth, epochDay = 1970, time.January, 1

// ---------------------------------------------------------------------------
// Per-subtype parsing (LeafFieldParser.kt formatters)
// ---------------------------------------------------------------------------

// localDateTimeLayouts are the offset-less ISO_DATE_TIME shapes we accept.
// ISO_DATE_TIME also tolerates an offset (which it then ignores); that case is
// covered by trying RFC3339 first.
var localDateTimeLayouts = []string{
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
}

var localTimeLayouts = []string{
	"15:04:05.999999999",
	"15:04:05",
	"15:04",
}

// ParseTemporalSubtype parses operand as exactly the temporal subtype t,
// returning ok=false if the string is not a valid representation of that
// subtype. ZonedDateTime requires an explicit offset (Z or +/-hh:mm); an
// offset-less string is rejected (the data-field offset-mandatory rule). t must
// be one of the six temporal subtypes; any other DataType yields ok=false.
func ParseTemporalSubtype(operand string, t DataType) (TemporalValue, bool) {
	switch t {
	case Year:
		if !isAllDigits(operand) || len(trimSign(operand)) < 4 {
			return TemporalValue{}, false
		}
		tt, err := time.Parse("2006", operand)
		if err != nil {
			return TemporalValue{}, false
		}
		return zoneLess(Year, tt), true

	case YearMonth:
		tt, err := time.Parse("2006-01", operand)
		if err != nil {
			return TemporalValue{}, false
		}
		return zoneLess(YearMonth, tt), true

	case LocalDate:
		tt, err := time.Parse("2006-01-02", operand)
		if err != nil {
			return TemporalValue{}, false
		}
		return zoneLess(LocalDate, tt), true

	case LocalTime:
		for _, layout := range localTimeLayouts {
			if tt, err := time.Parse(layout, operand); err == nil {
				// Go anchors a time-only parse to year 0; move it to EPOCH.
				w := time.Date(epochYear, epochMonth, epochDay,
					tt.Hour(), tt.Minute(), tt.Second(), tt.Nanosecond(), time.UTC)
				return TemporalValue{Type: LocalTime, wall: w}, true
			}
		}
		return TemporalValue{}, false

	case LocalDateTime:
		// ISO_DATE_TIME accepts an optional offset; honour that by trying an
		// offset-bearing parse first and keeping only the wall-clock fields.
		if tt, err := time.Parse(time.RFC3339Nano, operand); err == nil {
			return zoneLess(LocalDateTime, wallOf(tt)), true
		}
		for _, layout := range localDateTimeLayouts {
			if tt, err := time.Parse(layout, operand); err == nil {
				return zoneLess(LocalDateTime, tt), true
			}
		}
		return TemporalValue{}, false

	case ZonedDateTime:
		// Requires an explicit offset. RFC3339 mandates one, so an offset-less
		// string fails here.
		tt, err := time.Parse(time.RFC3339Nano, operand)
		if err != nil {
			return TemporalValue{}, false
		}
		_, off := tt.Zone()
		return TemporalValue{
			Type:       ZonedDateTime,
			wall:       wallOf(tt),
			offsetSecs: off,
			zoned:      true,
		}, true

	default:
		return TemporalValue{}, false
	}
}

// zoneLess builds a zone-less TemporalValue from a time.Time whose fields are
// already the intended wall-clock values, re-anchored at UTC.
func zoneLess(t DataType, tt time.Time) TemporalValue {
	return TemporalValue{Type: t, wall: wallOf(tt)}
}

// wallOf reads a time.Time's wall-clock fields (in its own location) and places
// them at UTC, discarding any offset.
func wallOf(tt time.Time) time.Time {
	return time.Date(tt.Year(), tt.Month(), tt.Day(),
		tt.Hour(), tt.Minute(), tt.Second(), tt.Nanosecond(), time.UTC)
}

func trimSign(s string) string {
	if len(s) > 0 && (s[0] == '+' || s[0] == '-') {
		return s[1:]
	}
	return s
}

func isAllDigits(s string) bool {
	s = trimSign(s)
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// naturalParseOrder is the precedence for classifying an operand to its natural
// subtype: the finest shape that parses wins. ZonedDateTime precedes
// LocalDateTime so an offset-bearing datetime keeps its offset (the two-way
// ambiguity noted in DataType.kt is resolved in favour of the more specific
// subtype).
var naturalParseOrder = []DataType{
	ZonedDateTime, LocalDateTime, LocalDate, LocalTime, YearMonth, Year,
}

// parseNatural classifies operand to its most specific temporal subtype.
func parseNatural(operand string) (TemporalValue, bool) {
	for _, t := range naturalParseOrder {
		if v, ok := ParseTemporalSubtype(operand, t); ok {
			return v, true
		}
	}
	return TemporalValue{}, false
}

// ---------------------------------------------------------------------------
// Resolution graph (PolymorphicTemporalConversions.kt)
// ---------------------------------------------------------------------------

// downEdge is one DownscaleTemporalConverter (kt:12-15,22-38): a value floor,
// a per-value precision test, and whether an imprecise hop may mutate the op.
type downEdge struct {
	to                DataType
	convert           func(TemporalValue) TemporalValue
	isPrecise         func(TemporalValue) bool
	modifyOnImprecise bool
}

// upEdge is one UpscaleTemporalConverter (kt:16-19,39-45): a value floor to the
// start of the finer period. It never carries precision or op-mutation.
type upEdge struct {
	to      DataType
	convert func(TemporalValue) TemporalValue
}

var downGraph = map[DataType][]downEdge{
	YearMonth: {{
		to:                Year,
		convert:           func(v TemporalValue) TemporalValue { return floorToYear(v) },
		isPrecise:         func(v TemporalValue) bool { return v.wall.Month() == time.January },
		modifyOnImprecise: true,
	}},
	LocalDate: {{
		to:                YearMonth,
		convert:           func(v TemporalValue) TemporalValue { return floorToYearMonth(v) },
		isPrecise:         func(v TemporalValue) bool { return v.wall.Day() == 1 },
		modifyOnImprecise: true,
	}},
	LocalDateTime: {
		{
			to:                LocalDate,
			convert:           func(v TemporalValue) TemporalValue { return floorToLocalDate(v) },
			isPrecise:         func(v TemporalValue) bool { return timeOfDayNanos(v.wall) == 0 },
			modifyOnImprecise: true,
		},
		{
			to:                LocalTime,
			convert:           func(v TemporalValue) TemporalValue { return floorToLocalTime(v) },
			isPrecise:         func(v TemporalValue) bool { return isEpochDate(v.wall) },
			modifyOnImprecise: false, // the sole modify=false edge (kt:33)
		},
	},
	ZonedDateTime: {{
		to:                LocalDateTime,
		convert:           func(v TemporalValue) TemporalValue { return dropZone(v) },
		isPrecise:         func(TemporalValue) bool { return true }, // zone dropped, always precise (kt:38)
		modifyOnImprecise: true,
	}},
}

var upGraph = map[DataType][]upEdge{
	Year:          {{to: YearMonth, convert: func(v TemporalValue) TemporalValue { return retag(floorToYearMonthStart(v), YearMonth) }}},
	YearMonth:     {{to: LocalDate, convert: func(v TemporalValue) TemporalValue { return retag(floorToDayStart(v), LocalDate) }}},
	LocalDate:     {{to: LocalDateTime, convert: func(v TemporalValue) TemporalValue { return retag(floorToMidnight(v), LocalDateTime) }}},
	LocalTime:     {{to: LocalDateTime, convert: func(v TemporalValue) TemporalValue { return atEpochDate(v) }}},
	LocalDateTime: {{to: ZonedDateTime, convert: func(v TemporalValue) TemporalValue { return atUTC(v) }}},
}

// --- floor helpers (downscale) ---

func floorToYear(v TemporalValue) TemporalValue {
	return TemporalValue{Type: Year, wall: time.Date(v.wall.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)}
}
func floorToYearMonth(v TemporalValue) TemporalValue {
	return TemporalValue{Type: YearMonth, wall: time.Date(v.wall.Year(), v.wall.Month(), 1, 0, 0, 0, 0, time.UTC)}
}
func floorToLocalDate(v TemporalValue) TemporalValue {
	return TemporalValue{Type: LocalDate, wall: time.Date(v.wall.Year(), v.wall.Month(), v.wall.Day(), 0, 0, 0, 0, time.UTC)}
}
func floorToLocalTime(v TemporalValue) TemporalValue {
	return TemporalValue{Type: LocalTime, wall: time.Date(epochYear, epochMonth, epochDay,
		v.wall.Hour(), v.wall.Minute(), v.wall.Second(), v.wall.Nanosecond(), time.UTC)}
}

// dropZone reproduces zdt.toLocalDateTime(): keep the wall-clock fields, discard
// the offset. Thereafter the value reads at UTC.
func dropZone(v TemporalValue) TemporalValue {
	return TemporalValue{Type: LocalDateTime, wall: v.wall}
}

// --- floor helpers (upscale: start-of-period) ---

func floorToYearMonthStart(v TemporalValue) TemporalValue { // Year.atMonth(1)
	return TemporalValue{wall: time.Date(v.wall.Year(), time.January, 1, 0, 0, 0, 0, time.UTC)}
}
func floorToDayStart(v TemporalValue) TemporalValue { // YearMonth.atDay(1)
	return TemporalValue{wall: time.Date(v.wall.Year(), v.wall.Month(), 1, 0, 0, 0, 0, time.UTC)}
}
func floorToMidnight(v TemporalValue) TemporalValue { // LocalDate.atTime(MIDNIGHT)
	return TemporalValue{wall: time.Date(v.wall.Year(), v.wall.Month(), v.wall.Day(), 0, 0, 0, 0, time.UTC)}
}

// atEpochDate reproduces LocalTime.atDate(EPOCH): the wall already carries the
// time-of-day on the EPOCH date, so this only re-tags to LOCAL_DATE_TIME.
func atEpochDate(v TemporalValue) TemporalValue {
	return TemporalValue{Type: LocalDateTime, wall: v.wall}
}

// atUTC reproduces LocalDateTime.atZone(UTC): same wall fields, now a live zoned
// value at offset 0 (so its instant equals the wall read at UTC).
func atUTC(v TemporalValue) TemporalValue {
	return TemporalValue{Type: ZonedDateTime, wall: v.wall, offsetSecs: 0, zoned: true}
}

func retag(v TemporalValue, t DataType) TemporalValue {
	v.Type = t
	return v
}

func timeOfDayNanos(t time.Time) int64 {
	return int64(t.Hour())*int64(time.Hour) + int64(t.Minute())*int64(time.Minute) +
		int64(t.Second())*int64(time.Second) + int64(t.Nanosecond())
}
func isEpochDate(t time.Time) bool {
	return t.Year() == epochYear && t.Month() == epochMonth && t.Day() == epochDay
}

// --- path finding (PathFinder DFS, kt:88-114) ---

var (
	downPaths = buildDownPaths()
	upPaths   = buildUpPaths()
)

type edgeKey struct{ from, to DataType }

func buildDownPaths() map[edgeKey][]downEdge {
	paths := map[edgeKey][]downEdge{}
	var dfs func(start, current DataType, soFar []downEdge)
	dfs = func(start, current DataType, soFar []downEdge) {
		for _, e := range downGraph[current] {
			newPath := append(append([]downEdge{}, soFar...), e)
			paths[edgeKey{start, e.to}] = newPath
			dfs(start, e.to, newPath)
		}
	}
	for node := range allNodes(downGraphNodes()) {
		dfs(node, node, nil)
	}
	return paths
}

func buildUpPaths() map[edgeKey][]upEdge {
	paths := map[edgeKey][]upEdge{}
	var dfs func(start, current DataType, soFar []upEdge)
	dfs = func(start, current DataType, soFar []upEdge) {
		for _, e := range upGraph[current] {
			newPath := append(append([]upEdge{}, soFar...), e)
			paths[edgeKey{start, e.to}] = newPath
			dfs(start, e.to, newPath)
		}
	}
	for node := range allNodes(upGraphNodes()) {
		dfs(node, node, nil)
	}
	return paths
}

func downGraphNodes() [][2]DataType {
	var out [][2]DataType
	for from, edges := range downGraph {
		for _, e := range edges {
			out = append(out, [2]DataType{from, e.to})
		}
	}
	return out
}
func upGraphNodes() [][2]DataType {
	var out [][2]DataType
	for from, edges := range upGraph {
		for _, e := range edges {
			out = append(out, [2]DataType{from, e.to})
		}
	}
	return out
}
func allNodes(pairs [][2]DataType) map[DataType]struct{} {
	set := map[DataType]struct{}{}
	for _, p := range pairs {
		set[p[0]] = struct{}{}
		set[p[1]] = struct{}{}
	}
	return set
}

// ---------------------------------------------------------------------------
// convert() (PolymorphicTemporalConversions.kt:49-76)
// ---------------------------------------------------------------------------

// TemporalSubCondition is one resolved per-type branch: the target subtype, the
// operand as floored epoch-millis, and the (possibly mutated) operation. It
// bridges to the instant kernel — Millis feeds CompareTemporal.
type TemporalSubCondition struct {
	Type   DataType
	Millis int64
	Op     FilterOp
}

// convertTemporal resolves value (currently of type from) to the target subtype
// to under op. It tries the downscale path first, then the upscale path;
// ok=false means no path, or an imprecise-EQUALS drop. from == to has no path
// and returns ok=false — the caller handles identity separately.
func convertTemporal(from, to DataType, value TemporalValue, op FilterOp) (TemporalSubCondition, bool) {
	if path, ok := downPaths[edgeKey{from, to}]; ok {
		mutating := value
		updatedOp := op
		for _, e := range path {
			if !e.isPrecise(mutating) {
				if op == FilterEq {
					return TemporalSubCondition{}, false // imprecise EQUALS drop (kt:54-56)
				}
				if e.modifyOnImprecise {
					updatedOp = mutateDownscaleOp(op) // reads original op, idempotent
				}
			}
			mutating = e.convert(mutating)
		}
		return TemporalSubCondition{Type: to, Millis: mutating.Millis(), Op: updatedOp}, true
	}
	if path, ok := upPaths[edgeKey{from, to}]; ok {
		mutating := value
		for _, e := range path {
			mutating = e.convert(mutating)
		}
		// Upscale: op is never mutated, never dropped (kt:70-76).
		return TemporalSubCondition{Type: to, Millis: mutating.Millis(), Op: op}, true
	}
	return TemporalSubCondition{}, false
}

// mutateDownscaleOp reproduces the op-mutation table (kt:60-64): a floored value
// turns GREATER_OR_EQUAL into GREATER_THAN and LESS_THAN into LESS_OR_EQUAL;
// every other operation is unchanged. Idempotent across hops.
func mutateDownscaleOp(op FilterOp) FilterOp {
	switch op {
	case FilterGte:
		return FilterGt
	case FilterLt:
		return FilterLte
	default:
		return op
	}
}

// ExpandTemporalOperand parses a single temporal operand and resolves it into
// one condition per declared temporal subtype for a polymorphic (or meta) field,
// faithfully porting PolymorphicTemporalConversions.parseTemporalConditionToPolyType
// (kt:8-9) plus convert() (kt:49-76).
//
// The operand is classified once to its natural subtype. For each declared type:
// the matching subtype yields an identity condition (value and op unchanged);
// every other declared type is resolved through the downscale/upscale graph.
// Branches with no path, and imprecise-EQUALS downscales, are dropped. An empty
// result means every temporal branch was dropped.
//
// Meta-vs-data ZonedDateTime: a coarse operand ("2024") is classified as Year
// and upscaled to the instant, which is the meta-field relaxation of the
// offset-mandatory rule. The stricter data-field rule — a ZonedDateTime operand
// must carry an offset — lives in ParseTemporalSubtype(operand, ZonedDateTime),
// which rejects offset-less input; a data-field caller enforces it by parsing
// directly against its declared type rather than relying on coarse upscale.
func ExpandTemporalOperand(operand string, declaredTemporal []DataType, op FilterOp) []TemporalSubCondition {
	src, ok := parseNatural(operand)
	if !ok {
		return nil
	}
	var out []TemporalSubCondition
	for _, t := range declaredTemporal {
		if !isTemporalSubtype(t) {
			continue
		}
		if t == src.Type {
			out = append(out, TemporalSubCondition{Type: t, Millis: src.Millis(), Op: op})
			continue
		}
		if cond, ok := convertTemporal(src.Type, t, src, op); ok {
			out = append(out, cond)
		}
	}
	return out
}

func isTemporalSubtype(t DataType) bool {
	switch t {
	case LocalDate, LocalDateTime, LocalTime, ZonedDateTime, Year, YearMonth:
		return true
	default:
		return false
	}
}

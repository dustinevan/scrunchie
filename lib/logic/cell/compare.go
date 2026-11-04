package cell

import (
	"bytes"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dustinevan/scrunchie/pkg/result"
)

// order groups PostgreSQL types that share a client-side ordering.
type order uint8

const (
	byText order = iota
	byPaddedText
	byBytes
	byBool
	byDecimal
	byFloat
	byInstant
	byInterval
)

func orderFor(column result.Column) order {
	switch column.OID {
	case 16:
		return byBool
	case 17:
		return byBytes
	case 20, 21, 23, 26, 1700:
		return byDecimal
	case 700, 701:
		return byFloat
	case 1082, 1114, 1184:
		return byInstant
	case 1186:
		return byInterval
	case 1042:
		return byPaddedText
	case 0:
		switch strings.ToLower(column.Type) {
		case "bool":
			return byBool
		case "bytea":
			return byBytes
		case "int2", "int4", "int8", "oid", "numeric":
			return byDecimal
		case "float4", "float8":
			return byFloat
		case "date", "timestamp", "timestamptz":
			return byInstant
		case "interval":
			return byInterval
		case "bpchar":
			return byPaddedText
		}
	}
	return byText
}

// Compare orders two values from one column the way PostgreSQL's default
// operators would for common built-in types: integers and numeric exactly,
// floats numerically with NaN greatest, booleans false first, ISO dates and
// timestamps chronologically, intervals by PostgreSQL's 30-day-month span,
// bytea by bytes, and other text byte-wise like the C collation. SQL NULL
// sorts after every value; reversing the result puts NULL first, matching
// PostgreSQL's ASC and DESC defaults. Unparsable text compares byte-wise.
func Compare(a, b result.Cell, column result.Column) int {
	kind := orderFor(column)
	if c, ok := compareAs(kind, a, b); ok {
		return c
	}
	c, _ := compareAs(byText, a, b)
	return c
}

// Sort stably orders row indices by one column. Mixing typed and byte-wise
// comparisons would not be a consistent order, so one value that does not
// parse as its type (for example a date in a non-ISO DateStyle) makes the
// whole column sort byte-wise. Rows shorter than the column sort as NULL.
func Sort(rows [][]result.Cell, indices []int, column int, metadata result.Column, descending bool) {
	kind := orderFor(metadata)
	for _, index := range indices {
		value := result.Cell{Null: true}
		if column < len(rows[index]) {
			value = rows[index][column]
		}
		if _, ok := compareAs(kind, value, value); !ok {
			kind = byText
			break
		}
	}
	sort.SliceStable(indices, func(i, j int) bool {
		a, b := result.Cell{Null: true}, result.Cell{Null: true}
		if column < len(rows[indices[i]]) {
			a = rows[indices[i]][column]
		}
		if column < len(rows[indices[j]]) {
			b = rows[indices[j]][column]
		}
		c, _ := compareAs(kind, a, b)
		if descending {
			return c > 0
		}
		return c < 0
	})
}

// compareAs reports false when either non-NULL value does not parse as kind.
func compareAs(kind order, a, b result.Cell) (int, bool) {
	if a.Null || b.Null {
		switch {
		case a.Null && b.Null:
			return 0, true
		case a.Null:
			return 1, true
		default:
			return -1, true
		}
	}
	switch kind {
	case byBytes:
		return bytes.Compare(a.Bytes, b.Bytes), true
	case byPaddedText:
		return strings.Compare(strings.TrimRight(a.Text, " "), strings.TrimRight(b.Text, " ")), true
	case byBool:
		x, xok := parseBool(a.Text)
		y, yok := parseBool(b.Text)
		if !xok || !yok {
			return 0, false
		}
		switch {
		case x == y:
			return 0, true
		case !x:
			return -1, true
		default:
			return 1, true
		}
	case byDecimal:
		x, xok := parseDecimal(a.Text)
		y, yok := parseDecimal(b.Text)
		if !xok || !yok {
			return 0, false
		}
		return compareDecimal(x, y), true
	case byFloat:
		x, xerr := strconv.ParseFloat(a.Text, 64)
		y, yerr := strconv.ParseFloat(b.Text, 64)
		if xerr != nil || yerr != nil {
			return 0, false
		}
		switch {
		case math.IsNaN(x) && math.IsNaN(y):
			return 0, true
		case math.IsNaN(x):
			return 1, true
		case math.IsNaN(y):
			return -1, true
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		// Negative zero equals zero, as it does in PostgreSQL.
		return 0, true
	case byInstant:
		x, xok := parseInstant(a.Text)
		y, yok := parseInstant(b.Text)
		if !xok || !yok {
			return 0, false
		}
		if x.class != y.class {
			return compareInt(int64(x.class), int64(y.class)), true
		}
		return x.at.Compare(y.at), true
	case byInterval:
		x, xok := parseInterval(a.Text)
		y, yok := parseInterval(b.Text)
		if !xok || !yok {
			return 0, false
		}
		if x.class != y.class {
			return compareInt(int64(x.class), int64(y.class)), true
		}
		if x.days != y.days {
			return compareInt(x.days, y.days), true
		}
		return compareInt(x.micros, y.micros), true
	}
	return strings.Compare(a.Text, b.Text), true
}

func compareInt(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Text output uses t/f; binary results decoded by the driver use true/false.
func parseBool(text string) (bool, bool) {
	switch text {
	case "t", "true":
		return true, true
	case "f", "false":
		return false, true
	}
	return false, false
}

// decimal is 0.digits × 10^point with no leading or trailing zero digits.
// class orders the special values: -1 -Infinity, 0 finite, 1 Infinity, 2 NaN.
type decimal struct {
	class    int8
	negative bool
	digits   string
	point    int
}

func parseDecimal(text string) (decimal, bool) {
	switch strings.ToLower(text) {
	case "nan":
		return decimal{class: 2}, true
	case "infinity", "+infinity":
		return decimal{class: 1}, true
	case "-infinity":
		return decimal{class: -1}, true
	}
	var d decimal
	i := 0
	if i < len(text) && (text[i] == '-' || text[i] == '+') {
		d.negative = text[i] == '-'
		i++
	}
	start := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' {
		i++
	}
	whole := text[start:i]
	fraction := ""
	if i < len(text) && text[i] == '.' {
		i++
		start = i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		fraction = text[start:i]
	}
	if whole == "" && fraction == "" {
		return decimal{}, false
	}
	exponent := 0
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		start = i
		if i < len(text) && (text[i] == '-' || text[i] == '+') {
			i++
		}
		digits := i
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
		if i == digits || i-digits > 9 {
			return decimal{}, false
		}
		value, err := strconv.Atoi(text[start:i])
		if err != nil {
			return decimal{}, false
		}
		exponent = value
	}
	if i != len(text) {
		return decimal{}, false
	}
	d.digits = whole + fraction
	d.point = len(whole) + exponent
	for len(d.digits) > 0 && d.digits[0] == '0' {
		d.digits = d.digits[1:]
		d.point--
	}
	d.digits = strings.TrimRight(d.digits, "0")
	if d.digits == "" {
		// Zero has no sign: -0.00 equals 0.
		return decimal{}, true
	}
	return d, true
}

func compareDecimal(a, b decimal) int {
	if a.class != b.class {
		return compareInt(int64(a.class), int64(b.class))
	}
	if a.class != 0 {
		return 0
	}
	aSign, bSign := 1, 1
	if a.digits == "" {
		aSign = 0
	} else if a.negative {
		aSign = -1
	}
	if b.digits == "" {
		bSign = 0
	} else if b.negative {
		bSign = -1
	}
	if aSign != bSign || aSign == 0 {
		return compareInt(int64(aSign), int64(bSign))
	}
	magnitude := compareInt(int64(a.point), int64(b.point))
	if magnitude == 0 {
		// Without trailing zeros, digit strings compare like the fractions they are.
		magnitude = strings.Compare(a.digits, b.digits)
	}
	return magnitude * aSign
}

// instant is a date, timestamp, or timestamptz in UTC. class orders
// -infinity (-1), finite values (0), and infinity (1).
type instant struct {
	class int8
	at    time.Time
}

// parseInstant reads PostgreSQL's ISO DateStyle output: YYYY-MM-DD, an
// optional HH:MM:SS[.fraction], an optional UTC offset, and an optional BC.
func parseInstant(text string) (instant, bool) {
	switch text {
	case "infinity":
		return instant{class: 1}, true
	case "-infinity":
		return instant{class: -1}, true
	}
	bc := strings.HasSuffix(text, " BC")
	text = strings.TrimSuffix(text, " BC")
	year, i, ok := number(text, 0, 0)
	if !ok || i >= len(text) || text[i] != '-' {
		return instant{}, false
	}
	month, i, ok := number(text, i+1, 2)
	if !ok || i >= len(text) || text[i] != '-' {
		return instant{}, false
	}
	day, i, ok := number(text, i+1, 2)
	if !ok {
		return instant{}, false
	}
	var hour, minute, second, nanos int
	var offset time.Duration
	if i < len(text) {
		if text[i] != ' ' {
			return instant{}, false
		}
		hour, i, ok = number(text, i+1, 2)
		if !ok || i >= len(text) || text[i] != ':' {
			return instant{}, false
		}
		minute, i, ok = number(text, i+1, 2)
		if !ok || i >= len(text) || text[i] != ':' {
			return instant{}, false
		}
		second, i, ok = number(text, i+1, 2)
		if !ok {
			return instant{}, false
		}
		if i < len(text) && text[i] == '.' {
			start := i + 1
			i = start
			for i < len(text) && text[i] >= '0' && text[i] <= '9' {
				i++
			}
			digits := text[start:i]
			if digits == "" || len(digits) > 9 {
				return instant{}, false
			}
			nanos, _ = strconv.Atoi(digits + strings.Repeat("0", 9-len(digits)))
		}
		if i < len(text) {
			if text[i] != '+' && text[i] != '-' {
				return instant{}, false
			}
			sign := time.Duration(1)
			if text[i] == '-' {
				sign = -1
			}
			parts := []time.Duration{time.Hour, time.Minute, time.Second}
			for part := 0; part < len(parts) && i < len(text); part++ {
				if part > 0 && text[i] != ':' {
					return instant{}, false
				}
				var value int
				value, i, ok = number(text, i+1, 2)
				if !ok {
					return instant{}, false
				}
				offset += time.Duration(value) * parts[part]
			}
			offset *= sign
		}
		if i != len(text) {
			return instant{}, false
		}
	}
	if bc {
		// 1 BC is astronomical year 0.
		year = 1 - year
	}
	at := time.Date(year, time.Month(month), day, hour, minute, second, nanos, time.UTC).Add(-offset)
	return instant{at: at}, true
}

// number reads exactly width decimal digits at i, or 1 to 9 when width is 0.
func number(text string, i, width int) (int, int, bool) {
	start := i
	for i < len(text) && text[i] >= '0' && text[i] <= '9' && (width == 0 || i-start < width) {
		i++
	}
	if i == start || width > 0 && i-start != width || i-start > 9 {
		return 0, i, false
	}
	value, err := strconv.Atoi(text[start:i])
	return value, i, err == nil
}

// interval is PostgreSQL's comparison span (30-day months, 24-hour days)
// split into whole days and nonnegative microseconds so it cannot overflow.
type interval struct {
	class  int8
	days   int64
	micros int64
}

const microsPerDay = 24 * 60 * 60 * 1000000

// parseInterval reads the default "postgres" IntervalStyle, such as
// "1 year 2 mons -3 days +04:05:06.5". Other styles fall back to text.
func parseInterval(text string) (interval, bool) {
	switch text {
	case "infinity":
		return interval{class: 1}, true
	case "-infinity":
		return interval{class: -1}, true
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return interval{}, false
	}
	var months, days, micros int64
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		if strings.Contains(field, ":") {
			negative := strings.HasPrefix(field, "-")
			field = strings.TrimLeft(field, "+-")
			parts := strings.Split(field, ":")
			if len(parts) != 3 {
				return interval{}, false
			}
			hours, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil || hours < 0 || hours > math.MaxInt64/3600000000 {
				return interval{}, false
			}
			minutes, err := strconv.ParseInt(parts[1], 10, 64)
			if err != nil || minutes < 0 || minutes > 59 {
				return interval{}, false
			}
			whole, fraction, _ := strings.Cut(parts[2], ".")
			seconds, err := strconv.ParseInt(whole, 10, 64)
			if err != nil || seconds < 0 || seconds > 59 || len(fraction) > 6 {
				return interval{}, false
			}
			var fractional int64
			if fraction != "" {
				fractional, err = strconv.ParseInt(fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
				if err != nil || fractional < 0 {
					return interval{}, false
				}
			}
			value := hours*3600000000 + minutes*60000000 + seconds*1000000 + fractional
			if negative {
				value = -value
			}
			micros += value
			continue
		}
		if i+1 >= len(fields) {
			return interval{}, false
		}
		amount, err := strconv.ParseInt(field, 10, 64)
		if err != nil || amount > math.MaxInt32 || amount < math.MinInt32 {
			return interval{}, false
		}
		i++
		switch fields[i] {
		case "year", "years":
			months += amount * 12
		case "mon", "mons":
			months += amount
		case "day", "days":
			days += amount
		default:
			return interval{}, false
		}
	}
	total := months*30 + days + micros/microsPerDay
	remainder := micros % microsPerDay
	if remainder < 0 {
		remainder += microsPerDay
		total--
	}
	return interval{days: total, micros: remainder}, true
}

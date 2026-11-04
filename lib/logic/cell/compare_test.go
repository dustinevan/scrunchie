package cell

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dustinevan/scrunchie/pkg/result"
)

// Each list is in ascending PostgreSQL order; equal neighbors are grouped.
func TestCompareOrdersCommonTypesLikePostgreSQL(t *testing.T) {
	for _, tc := range []struct {
		name   string
		column result.Column
		groups [][]string
	}{
		{"int8", result.Column{OID: 20}, [][]string{{"-9223372036854775808"}, {"-10"}, {"-9"}, {"0"}, {"9"}, {"10"}, {"100"}, {"9223372036854775807"}}},
		{"numeric", result.Column{OID: 1700}, [][]string{
			{"-Infinity"}, {"-123456789012345678901234567890.5"}, {"-1.5"}, {"-1.25"}, {"-0.00", "0", "0.000"},
			{"0.000000000000000000000000000001"}, {"0.45"}, {"0.5", "0.50", "5e-1"}, {"12.5", "00012.50", "1.25E1"},
			{"123456789012345678901234567890.09"}, {"123456789012345678901234567890.1"}, {"Infinity"}, {"NaN", "nan"},
		}},
		{"float8", result.Column{OID: 701}, [][]string{
			{"-Infinity"}, {"-1.7976931348623157e+308"}, {"-1e-07"}, {"-0", "0"}, {"5e-324"}, {"1.5"}, {"15"}, {"1e+20"}, {"Infinity"}, {"NaN"},
		}},
		{"float4 by name", result.Column{Type: "float4"}, [][]string{{"-3.4028235e+38"}, {"2.5"}, {"NaN"}}},
		{"bool", result.Column{OID: 16}, [][]string{{"f", "false"}, {"t", "true"}}},
		{"date", result.Column{OID: 1082}, [][]string{
			{"-infinity"}, {"4714-11-24 BC"}, {"0044-03-15 BC"}, {"0001-12-31 BC"}, {"0001-01-01"}, {"1999-12-31"}, {"2024-02-29"}, {"10000-01-01"}, {"infinity"},
		}},
		{"timestamp", result.Column{OID: 1114}, [][]string{
			{"-infinity"}, {"2024-01-01 00:00:00"}, {"2024-01-01 00:00:00.000001"}, {"2024-01-01 00:00:00.5", "2024-01-01 00:00:00.500"}, {"2024-01-01 23:59:59"}, {"infinity"},
		}},
		// Text order would put +12 first; +12 is the later instant after a fall-back.
		{"timestamptz", result.Column{OID: 1184}, [][]string{
			{"0044-03-15 09:58:45-00:01:15 BC"}, {"2024-04-07 02:30:00+13"}, {"2024-04-07 02:30:00+12"}, {"2024-04-06 23:00:00-05", "2024-04-07 04:00:00+00"},
		}},
		{"interval", result.Column{OID: 1186}, [][]string{
			{"-1 years"}, {"-1 days +02:00:00"}, {"-00:00:00.5"}, {"00:00:00"}, {"00:00:01"}, {"1 day", "24:00:00"}, {"29 days"}, {"1 mon", "30 days"},
			{"1 year", "11 mons 30 days"}, {"178000000 years"},
		}},
		{"text", result.Column{OID: 25}, [][]string{{""}, {"A"}, {"B"}, {"a"}, {"a\tb"}, {"ab"}, {"z"}, {"é"}, {"雪"}}},
		{"bpchar", result.Column{OID: 1042}, [][]string{{"a", "a "}, {"ab"}, {"b "}}},
		{"uuid", result.Column{OID: 2950}, [][]string{{"00000000-0000-0000-0000-000000000000"}, {"0f000000-0000-0000-0000-000000000000"}, {"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"}}},
		{"jsonb as text", result.Column{OID: 3802}, [][]string{{`{"a": 10}`}, {`{"a": 1}`}, {`{"b": 0}`}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i, group := range tc.groups {
				for j, earlier := range tc.groups {
					for _, a := range group {
						for _, b := range earlier {
							want := 0
							if i < j {
								want = -1
							} else if i > j {
								want = 1
							}
							if got := Compare(result.Cell{Text: a}, result.Cell{Text: b}, tc.column); got != want {
								t.Errorf("Compare(%q, %q) = %d, want %d", a, b, got, want)
							}
						}
					}
				}
			}
		})
	}
}

func TestCompareBytesAndNull(t *testing.T) {
	bytea := result.Column{Type: "bytea", OID: 17}
	ordered := []result.Cell{{Bytes: []byte{}}, {Bytes: []byte{0}}, {Bytes: []byte{0, 1}}, {Bytes: []byte{1}}, {Bytes: []byte{0xff}}, {Null: true}}
	for i := range ordered {
		for j := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(ordered[i], ordered[j], bytea); got != want {
				t.Errorf("Compare(%v, %v) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	text := result.Column{OID: 25}
	if Compare(result.Cell{Null: true}, result.Cell{Text: ""}, text) != 1 || Compare(result.Cell{Text: "NULL"}, result.Cell{Null: true}, text) != -1 {
		t.Fatal("SQL NULL did not sort after empty text and the string NULL")
	}
}

func TestSortIsStableWithPostgreSQLNullPlacement(t *testing.T) {
	rows := [][]result.Cell{
		{{Text: "10"}, {Text: "first ten"}},
		{{Null: true}, {Text: "first null"}},
		{{Text: "9"}, {Text: "nine"}},
		{{Text: "10.0"}, {Text: "second ten"}},
		{{Null: true}, {Text: "second null"}},
		{{Text: "-1"}},
	}
	labels := func(order []int) string {
		var names []string
		for _, index := range order {
			if len(rows[index]) > 1 {
				names = append(names, rows[index][1].Text)
			} else {
				names = append(names, "short")
			}
		}
		return strings.Join(names, ",")
	}
	order := []int{0, 1, 2, 3, 4, 5}
	Sort(rows, order, 0, result.Column{OID: 1700}, false)
	if got := labels(order); got != "short,nine,first ten,second ten,first null,second null" {
		t.Fatalf("ascending = %s", got)
	}
	order = []int{0, 1, 2, 3, 4, 5}
	Sort(rows, order, 0, result.Column{OID: 1700}, true)
	if got := labels(order); got != "first null,second null,first ten,second ten,nine,short" {
		t.Fatalf("descending = %s", got)
	}
	// A missing cell sorts as NULL.
	order = []int{5, 0}
	Sort(rows, order, 1, result.Column{OID: 25}, false)
	if fmt.Sprint(order) != "[0 5]" {
		t.Fatalf("short row order = %v", order)
	}
}

func TestSortFallsBackToTextForWholeColumnWhenAValueDoesNotParse(t *testing.T) {
	// A non-ISO DateStyle cannot be parsed; mixing chronological and byte-wise
	// comparisons would not be an order, so the column sorts as text.
	rows := [][]result.Cell{{{Text: "2024-01-02"}}, {{Text: "12/31/2023"}}, {{Text: "2023-12-31"}}}
	order := []int{0, 1, 2}
	Sort(rows, order, 0, result.Column{OID: 1082}, false)
	if fmt.Sprint(order) != "[1 2 0]" {
		t.Fatalf("fallback order = %v", order)
	}
	order = []int{0, 2}
	Sort(rows, order, 0, result.Column{OID: 1082}, false)
	if fmt.Sprint(order) != "[2 0]" {
		t.Fatalf("parsed order = %v", order)
	}
}

func TestSortTenThousandRows(t *testing.T) {
	rows := make([][]result.Cell, 10000)
	order := make([]int, len(rows))
	for i := range rows {
		rows[i] = []result.Cell{{Text: fmt.Sprintf("%d.%02d", (i*7919)%10000, i%100)}}
		order[i] = i
	}
	Sort(rows, order, 0, result.Column{OID: 1700}, false)
	for i := 1; i < len(order); i++ {
		if Compare(rows[order[i-1]][0], rows[order[i]][0], result.Column{OID: 1700}) > 0 {
			t.Fatalf("rows %d and %d are out of order", order[i-1], order[i])
		}
	}
}

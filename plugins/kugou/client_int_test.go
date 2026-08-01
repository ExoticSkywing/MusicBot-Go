package kugou

import (
	"encoding/json"
	"math"
	"testing"
)

// TestParseKugouIntClampsOutOfRange pins the narrowing behaviour of
// parseKugouInt. The values come from a remote JSON payload, so on 32-bit
// builds a plain int64->int conversion would wrap a large upstream number into
// a negative count. The expectations below hold on both 32- and 64-bit targets:
// math.MaxInt is the platform's own ceiling.
func TestParseKugouIntClampsOutOfRange(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value any
		want  int
	}{
		{name: "float64", value: float64(42), want: 42},
		{name: "int", value: 7, want: 7},
		{name: "int64", value: int64(1234), want: 1234},
		{name: "json number", value: json.Number("5678"), want: 5678},
		{name: "numeric string", value: "910", want: 910},
		{name: "negative string", value: "-11", want: -11},
		{name: "unparsable", value: "not-a-number", want: 0},
		{name: "unsupported type", value: struct{}{}, want: 0},
		{name: "above platform ceiling", value: int64(math.MaxInt64), want: math.MaxInt},
		{name: "below platform floor", value: int64(math.MinInt64), want: math.MinInt},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseKugouInt(tt.value); got != tt.want {
				t.Errorf("parseKugouInt(%v) = %d, want %d", tt.value, got, tt.want)
			}
		})
	}
}

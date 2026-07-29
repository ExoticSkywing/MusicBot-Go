package kuwo

import (
	"encoding/json"
	"testing"
)

func decodeScalar(t *testing.T, value string) jsonScalar {
	t.Helper()
	var wrapped struct {
		Value jsonScalar `json:"value"`
	}
	if err := json.Unmarshal([]byte(`{"value":`+value+`}`), &wrapped); err != nil {
		t.Fatalf("decode %s: %v", value, err)
	}
	return wrapped.Value
}

func TestJSONScalar(t *testing.T) {
	for _, input := range []string{"200", `"200"`} {
		s := decodeScalar(t, input)
		if got, ok := s.Int64(); !ok || got != 200 {
			t.Errorf("Int64(%s) = %d, %v; want 200, true", input, got, ok)
		}
		if got, ok := s.String(); !ok || got != "200" {
			t.Errorf("String(%s) = %q, %v; want 200, true", input, got, ok)
		}
		if _, ok := s.Bool(); ok {
			t.Errorf("Bool(%s) unexpectedly converted", input)
		}
	}

	for _, input := range []string{"true", `"true"`} {
		s := decodeScalar(t, input)
		if got, ok := s.Bool(); !ok || !got {
			t.Errorf("Bool(%s) = %v, %v; want true, true", input, got, ok)
		}
		if got, ok := s.String(); !ok || got != "true" {
			t.Errorf("String(%s) = %q, %v; want true, true", input, got, ok)
		}
		if _, ok := s.Int64(); ok {
			t.Errorf("Int64(%s) unexpectedly converted", input)
		}
	}

	s := decodeScalar(t, "null")
	if _, ok := s.String(); ok {
		t.Error("null String unexpectedly converted")
	}
	if _, ok := s.Int64(); ok {
		t.Error("null Int64 unexpectedly converted")
	}
	if _, ok := s.Bool(); ok {
		t.Error("null Bool unexpectedly converted")
	}
}

func TestJSONScalarRejectsInvalidConversions(t *testing.T) {
	if _, ok := decodeScalar(t, "9223372036854775808").Int64(); ok {
		t.Error("overflowing integer unexpectedly converted")
	}

	for _, input := range []string{"[]", "{}"} {
		var s jsonScalar
		if err := json.Unmarshal([]byte(input), &s); err == nil {
			t.Errorf("composite %s unexpectedly decoded", input)
		}
	}

	var s jsonScalar
	if err := s.UnmarshalJSON([]byte("200 true")); err != nil {
		t.Fatalf("store trailing JSON: %v", err)
	}
	if _, ok := s.Int64(); ok {
		t.Error("trailing JSON unexpectedly converted")
	}
}

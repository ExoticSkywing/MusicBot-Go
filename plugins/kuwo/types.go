package kuwo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// jsonScalar preserves a JSON scalar so callers can tolerate upstream type
// changes such as a numeric ID being returned as either a JSON number or string.
type jsonScalar struct {
	raw json.RawMessage
}

func (s *jsonScalar) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		s.raw = nil
		return nil
	}
	if trimmed[0] == '[' || trimmed[0] == '{' {
		return fmt.Errorf("kuwo: scalar cannot be composite")
	}
	s.raw = append(s.raw[:0], trimmed...)
	return nil
}

// String returns the scalar's textual form for JSON strings, numbers, and booleans.
func (s jsonScalar) String() (string, bool) {
	value, ok := s.value()
	if !ok {
		return "", false
	}
	switch value := value.(type) {
	case string:
		return value, true
	case json.Number:
		return value.String(), true
	case bool:
		return strconv.FormatBool(value), true
	default:
		return "", false
	}
}

// Int64 returns an integer scalar without accepting fractional or overflowing values.
func (s jsonScalar) Int64() (int64, bool) {
	value, ok := s.value()
	if !ok {
		return 0, false
	}
	var text string
	switch value := value.(type) {
	case string:
		text = value
	case json.Number:
		text = value.String()
	default:
		return 0, false
	}
	integer, err := strconv.ParseInt(text, 10, 64)
	return integer, err == nil
}

// Bool returns boolean scalars, including their string representation.
func (s jsonScalar) Bool() (bool, bool) {
	value, ok := s.value()
	if !ok {
		return false, false
	}
	switch value := value.(type) {
	case bool:
		return value, true
	case string:
		switch value {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}
	return false, false
}

func (s jsonScalar) value() (any, bool) {
	if len(s.raw) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(s.raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, false
	}
	switch value.(type) {
	case string, json.Number, bool:
		return value, true
	default:
		return nil, false
	}
}

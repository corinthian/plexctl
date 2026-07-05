// Package jsonx mirrors Python-dict semantics for the port: PMS JSON is
// handled as map[string]any end-to-end, matching plex-voice's dict handling
// so projections stay mechanical line-for-line.
package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// J is the port's equivalent of a Python dict parsed from JSON.
type J = map[string]any

// GetMap returns m[key] as a J, or an empty J — Python `d.get(k, {})`.
func GetMap(m J, key string) J {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return J{}
}

// GetList returns m[key] as a raw slice — Python `d.get(k, []) or []`.
func GetList(m J, key string) []any {
	if v, ok := m[key].([]any); ok {
		return v
	}
	return nil
}

// Maps filters a raw list down to its object elements.
func Maps(list []any) []J {
	out := make([]J, 0, len(list))
	for _, v := range list {
		if m, ok := v.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// MapList is GetList + Maps — Python `d.get(k, []) or []` iterated as dicts.
func MapList(m J, key string) []J {
	return Maps(GetList(m, key))
}

// Truthy mirrors Python truthiness: nil, false, 0, "", empty list/map → false.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case json.Number:
		f, err := t.Float64()
		return err != nil || f != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// Num coerces a JSON number to float64; missing or non-numeric → 0
// (Python `d.get(k, 0)` in sort keys and counters).
func Num(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case json.Number:
		f, err := t.Float64()
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

// AsStr renders a scalar the way Python str() renders JSON scalars: integral
// floats print without a decimal point (playQueueID 5535.0 → "5535"). Only
// meant for strings and numbers; other types are best-effort.
func AsStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		if t == math.Trunc(t) && math.Abs(t) < 1e15 {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return fmt.Sprint(v)
}

// Marshal renders v as one line of JSON without HTML escaping, matching
// Python json.dumps semantics (map keys come out sorted, which consumers
// treat as insignificant).
func Marshal(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		// Values are built from parsed JSON + scalars; this cannot fire in
		// practice, but never emit a non-JSON line.
		return `{"ok": false, "error": "internal: unmarshalable result"}`
	}
	return strings.TrimRight(buf.String(), "\n")
}

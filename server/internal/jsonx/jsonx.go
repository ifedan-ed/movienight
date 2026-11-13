// Package jsonx reads loosely typed JSON (map[string]any, as encoding/json
// decodes an upstream's answer) without a type switch at every use. Each
// reader answers the zero value and false for a missing key or a value of
// another type, so callers say what absence means instead of panicking on
// a service that changed a field's type.
package jsonx

import (
	"math"
	"strconv"
)

// Obj is one JSON object.
type Obj = map[string]any

// Str is m[k] as a string ("" when absent or not a string).
func Str(m Obj, k string) string {
	s, _ := m[k].(string)
	return s
}

// Int is m[k] as a whole number. JSON numbers decode as float64; a string
// of digits counts too (TMDB and Jellyfin send provider ids as strings).
func Int(m Obj, k string) (int, bool) {
	switch v := m[k].(type) {
	case float64:
		if v != math.Trunc(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return int(v), true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	return 0, false
}

// IntPtr is Int as a pointer (nil when absent), for nullable columns.
func IntPtr(m Obj, k string) *int32 {
	n, ok := Int(m, k)
	if !ok {
		return nil
	}
	v := int32(n)
	return &v
}

// Float is m[k] as a number.
func Float(m Obj, k string) (float64, bool) {
	f, ok := m[k].(float64)
	return f, ok
}

// Bool is m[k] as a boolean (false when absent).
func Bool(m Obj, k string) bool {
	b, _ := m[k].(bool)
	return b
}

// Child is the object at m[k] (nil when absent or not an object).
func Child(m Obj, k string) Obj {
	c, _ := m[k].(map[string]any)
	return c
}

// List is the array of objects at m[k], skipping anything that is not an
// object.
func List(m Obj, k string) []Obj {
	arr, _ := m[k].([]any)
	out := make([]Obj, 0, len(arr))
	for _, x := range arr {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

// Strings is the array of strings at m[k], skipping anything else.
func Strings(m Obj, k string) []string {
	arr, _ := m[k].([]any)
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// Ints is the array of whole numbers at m[k].
func Ints(m Obj, k string) []int {
	arr, _ := m[k].([]any)
	out := make([]int, 0, len(arr))
	for _, x := range arr {
		if f, ok := x.(float64); ok && f == math.Trunc(f) {
			out = append(out, int(f))
		}
	}
	return out
}

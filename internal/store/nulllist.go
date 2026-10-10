package store

import "encoding/json"

// NullList is a list whose nil means something else than empty (a default,
// "all"): it encodes as null, even through the API, whose encoder turns
// other nil lists into [] (api.writeJSON).
type NullList[T any] []T

// MarshalJSON writes null for nil and the list otherwise.
func (l NullList[T]) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("null"), nil
	}
	return json.Marshal([]T(l))
}

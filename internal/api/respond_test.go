package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ridoysheikh/syncloud/internal/dbs"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Clients iterate lists without null checks: an empty list or map is [] or
// {}, never null (a nil slice used to crash dashboard tabs).
func TestWriteJSONEmptyCollections(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, 200, struct {
		Items []string          `json:"items"`
		Tags  map[string]string `json:"tags"`
		Skip  []string          `json:"skip,omitempty"`
		Next  *string           `json:"next"`
		Text  string            `json:"text"`
	}{Text: "a → <b>"})
	if got, want := w.Body.String(), `{"items":[],"tags":{},"next":null,"text":"a → <b>"}`+"\n"; got != want {
		t.Errorf("got %s want %s", got, want)
	}
}

// Where nil means something else than empty, null survives: a cluster from
// before add-ons has every extension, and absent access is the default.
func TestWriteJSONMeaningfulNil(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, 200, map[string]any{"pg": dbs.PostgresSpec{}, "net": store.DatabaseNetwork{}, "none": dbs.PostgresSpec{Extensions: []string{}}})
	b := w.Body.String()
	if !strings.Contains(b, `"extensions":null`) || !strings.Contains(b, `"access":null`) || !strings.Contains(b, `"extensions":[]`) {
		t.Errorf("nil and empty mixed up: %s", b)
	}
	// Stored and read back by encoding/json as before.
	var back dbs.PostgresSpec
	raw, _ := json.Marshal(dbs.PostgresSpec{})
	if json.Unmarshal(raw, &back) != nil || back.Extensions != nil {
		t.Errorf("v1 round trip: %s", raw)
	}
}

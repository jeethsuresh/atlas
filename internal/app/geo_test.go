package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBusinessDiscoveryNormalizesOSMIdentitiesAndCaches(t *testing.T) {
	g := newGeo()
	calls := 0
	g.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "photon.komoot.io" || len(r.URL.Query()["osm_tag"]) < 3 || r.Header.Get("User-Agent") == "" {
			t.Fatal("unexpected business query")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"features":[{"geometry":{"coordinates":[-79.39,43.65]},"properties":{"osm_type":"N","osm_id":42,"osm_key":"amenity","osm_value":"cafe","name":"Coffee","street":"Queen Street","housenumber":"10"}},{"geometry":{"coordinates":[200,95]},"properties":{"name":"Invalid"}}]}`)), Header: http.Header{}}, nil
	})
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		g.places(w, httptest.NewRequest("GET", "/api/places?lat=43.651&lng=-79.391", nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct {
			Elements []struct {
				Type string            `json:"type"`
				ID   int64             `json:"id"`
				Tags map[string]string `json:"tags"`
			} `json:"elements"`
		}
		if json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Elements) != 1 || result.Elements[0].Type != "node" || result.Elements[0].ID != 42 || result.Elements[0].Tags["amenity"] != "cafe" {
			t.Fatal(w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal("business queries were not cached")
	}
}
func TestStreetGeometryDoesNotTurnPointsOrBuildingsIntoRoads(t *testing.T) {
	data := []byte(`[{"category":"highway","geojson":{"type":"LineString","coordinates":[[-79.4,43.65],[-79.3,43.66]]}},{"category":"building","geojson":{"type":"LineString","coordinates":[[-79,43],[-79,44]]}},{"category":"highway","geojson":{"type":"Point","coordinates":[-79,43]}}]`)
	raw, err := normalizeGeo("streets:test", data)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Elements []json.RawMessage `json:"elements"`
	}
	json.Unmarshal(raw, &result)
	if len(result.Elements) != 1 {
		t.Fatal(string(raw))
	}
}
func TestMapServiceRejectsUpstreamErrorsAndInvalidRequests(t *testing.T) {
	g := newGeo()
	g.client.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader(`busy`)), Header: http.Header{}}, nil
	})
	w := httptest.NewRecorder()
	g.places(w, httptest.NewRequest("GET", "/api/places?lat=43&lng=-79", nil))
	if w.Code != 502 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	g.places(w, httptest.NewRequest("GET", "/api/places?lat=NaN&lng=0", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	g.route(w, httptest.NewRequest("GET", "/api/route?lat=43&lng=-79&toLat=0&toLng=0", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}

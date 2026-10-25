package app

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type cached struct {
	data    []byte
	expires time.Time
}
type geoService struct {
	client     *http.Client
	mu         sync.Mutex
	cache      map[string]cached
	lastSearch time.Time
	slots      chan struct{}
}

func newGeo() *geoService {
	return &geoService{client: &http.Client{Timeout: 25 * time.Second}, cache: map[string]cached{}, slots: make(chan struct{}, 4)}
}
func (g *geoService) fetch(w http.ResponseWriter, r *http.Request, key, endpoint, body string, search bool) {
	g.mu.Lock()
	v, ok := g.cache[key]
	if ok && time.Now().Before(v.expires) {
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(v.data)
		return
	}
	if search {
		if time.Since(g.lastSearch) < time.Second {
			g.mu.Unlock()
			w.Header().Set("Retry-After", "1")
			fail(w, 429, "Please wait a moment before searching again")
			return
		}
		g.lastSearch = time.Now()
	}
	g.mu.Unlock()
	select {
	case g.slots <- struct{}{}:
		defer func() { <-g.slots }()
	default:
		fail(w, 429, "Map service is busy. Try again shortly.")
		return
	}
	method := "GET"
	if body != "" {
		method = "POST"
	}
	req, err := http.NewRequestWithContext(r.Context(), method, endpoint, strings.NewReader(body))
	if err != nil {
		fail(w, 500, "Unable to request map data")
		return
	}
	req.Header.Set("User-Agent", "Atlas/1.0 (https://maps.gravitas.cool; https://github.com/jeethsuresh/atlas)")
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := g.client.Do(req)
	if err != nil {
		fail(w, 502, "Map provider is unavailable. Try again shortly.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		fail(w, 502, "Map provider is busy. Try again shortly.")
		return
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (6<<20)+1))
	if err != nil || len(data) > 6<<20 || !json.Valid(data) {
		fail(w, 502, "Map provider returned an invalid response")
		return
	}
	if strings.HasPrefix(key, "places:") || strings.HasPrefix(key, "streets:") {
		data, err = normalizeGeo(key, data)
		if err != nil {
			fail(w, 502, "Map provider returned an invalid response")
			return
		}
	}
	g.mu.Lock()
	if len(g.cache) >= 128 {
		g.cache = map[string]cached{}
	}
	g.cache[key] = cached{data, time.Now().Add(5 * time.Minute)}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}
func (g *geoService) search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < 2 || len(q) > 160 {
		fail(w, 400, "Enter a place name between 2 and 160 characters")
		return
	}
	values := url.Values{"q": {q}, "format": {"jsonv2"}, "limit": {"7"}, "addressdetails": {"1"}}
	if lat, lng, ok := center(r); ok {
		values.Set("viewbox", fmt.Sprintf("%f,%f,%f,%f", lng-.1, lat+.1, lng+.1, lat-.1))
	}
	g.fetch(w, r, "search:"+values.Encode(), "https://nominatim.openstreetmap.org/search?"+values.Encode(), "", true)
}
func center(r *http.Request) (float64, float64, bool) {
	lat, a := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lng, b := strconv.ParseFloat(r.URL.Query().Get("lng"), 64)
	return lat, lng, a == nil && b == nil && validCoord(lat, lng)
}
func (g *geoService) places(w http.ResponseWriter, r *http.Request) {
	lat, lng, ok := center(r)
	if !ok {
		fail(w, 400, "Invalid map center")
		return
	}
	lat = float64(int(lat*1000)) / 1000
	lng = float64(int(lng*1000)) / 1000
	q := url.Values{"lat": {fmt.Sprintf("%.3f", lat)}, "lon": {fmt.Sprintf("%.3f", lng)}, "radius": {"2"}, "limit": {"50"}, "osm_tag": {"amenity:cafe", "amenity:restaurant", "amenity:bar", "amenity:pub", "amenity:fast_food", "amenity:pharmacy", "shop"}}
	g.fetch(w, r, "places:"+q.Encode(), "https://photon.komoot.io/reverse?"+q.Encode(), "", false)
}
func (g *geoService) streets(w http.ResponseWriter, r *http.Request) {
	lat, lng, ok := center(r)
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if !ok || len(name) < 2 || len(name) > 100 {
		fail(w, 400, "Enter a street name near the map center")
		return
	}
	q := url.Values{"q": {name}, "format": {"jsonv2"}, "limit": {"5"}, "polygon_geojson": {"1"}, "viewbox": {fmt.Sprintf("%f,%f,%f,%f", lng-.06, lat+.045, lng+.06, lat-.045)}, "bounded": {"1"}}
	g.fetch(w, r, "streets:"+q.Encode(), "https://nominatim.openstreetmap.org/search?"+q.Encode(), "", true)
}

func normalizeGeo(key string, data []byte) ([]byte, error) {
	elements := []map[string]any{}
	if strings.HasPrefix(key, "places:") {
		var collection struct {
			Features []struct {
				Geometry struct {
					Coordinates []float64 `json:"coordinates"`
				} `json:"geometry"`
				Properties struct {
					ID     int64  `json:"osm_id"`
					Type   string `json:"osm_type"`
					Key    string `json:"osm_key"`
					Value  string `json:"osm_value"`
					Name   string `json:"name"`
					Street string `json:"street"`
					Number string `json:"housenumber"`
				} `json:"properties"`
			} `json:"features"`
		}
		if err := json.Unmarshal(data, &collection); err != nil {
			return nil, err
		}
		for _, f := range collection.Features {
			p := f.Properties
			c := f.Geometry.Coordinates
			if len(c) != 2 || !validCoord(c[1], c[0]) || p.Name == "" {
				continue
			}
			kind := map[string]string{"N": "node", "W": "way", "R": "relation"}[p.Type]
			if kind == "" {
				continue
			}
			elements = append(elements, map[string]any{"type": kind, "id": p.ID, "lat": c[1], "lon": c[0], "tags": map[string]string{"name": p.Name, p.Key: p.Value, "addr:street": p.Street, "addr:housenumber": p.Number}})
		}
	} else {
		var results []struct {
			Category string `json:"category"`
			Class    string `json:"class"`
			GeoJSON  struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geojson"`
		}
		if err := json.Unmarshal(data, &results); err != nil {
			return nil, err
		}
		for _, p := range results {
			if p.Category != "highway" && p.Class != "highway" {
				continue
			}
			var lines [][][]float64
			switch p.GeoJSON.Type {
			case "LineString":
				var line [][]float64
				if json.Unmarshal(p.GeoJSON.Coordinates, &line) == nil {
					lines = append(lines, line)
				}
			case "MultiLineString":
				_ = json.Unmarshal(p.GeoJSON.Coordinates, &lines)
			}
			for _, line := range lines {
				points := []map[string]float64{}
				for _, c := range line {
					if len(c) == 2 && validCoord(c[1], c[0]) {
						points = append(points, map[string]float64{"lat": c[1], "lon": c[0]})
					}
				}
				if len(points) > 1 {
					elements = append(elements, map[string]any{"geometry": points})
				}
			}
		}
	}
	return json.Marshal(map[string]any{"elements": elements})
}
func (g *geoService) route(w http.ResponseWriter, r *http.Request) {
	lat, lng, ok := center(r)
	toLat, a := strconv.ParseFloat(r.URL.Query().Get("toLat"), 64)
	toLng, b := strconv.ParseFloat(r.URL.Query().Get("toLng"), 64)
	if !ok || a != nil || b != nil || !validCoord(toLat, toLng) {
		fail(w, 400, "Choose a valid start and destination")
		return
	}
	if abs(lat-toLat) > 3 || abs(lng-toLng) > 3 {
		fail(w, 400, "Choose a route within the surrounding region")
		return
	}
	endpoint := fmt.Sprintf("https://router.project-osrm.org/route/v1/driving/%f,%f;%f,%f?overview=full&geometries=geojson&steps=true", lng, lat, toLng, toLat)
	g.fetch(w, r, endpoint, endpoint, "", false)
}
func abs(n float64) float64 {
	if n < 0 {
		return -n
	}
	return n
}

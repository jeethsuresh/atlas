package storecatalog

import (
	"context"
	"embed"
	"encoding/json"
	"log"
)

//go:embed catalog.json *.png
var assets embed.FS

var service = New("atlas", []string{"atlas"}, "")

func Run(ctx context.Context) {
	if service == nil {
		return
	}
	raw, err := assets.ReadFile("catalog.json")
	if err != nil {
		log.Print("Private Store catalog is missing")
		return
	}
	var catalog struct {
		SchemaVersion int                          `json:"schemaVersion"`
		Revision      int64                        `json:"revision"`
		Apps          []map[string]json.RawMessage `json:"apps"`
	}
	if json.Unmarshal(raw, &catalog) != nil || len(catalog.Apps) != len(service.ids) {
		log.Print("Private Store catalog is invalid")
		return
	}
	for i, app := range catalog.Apps {
		icon, err := assets.ReadFile(service.ids[i] + ".png")
		if err != nil {
			log.Print("Private Store catalog icon is missing")
			return
		}
		app["logoPng"], _ = json.Marshal(icon)
	}
	payload, err := json.Marshal(catalog)
	if err != nil {
		log.Print("Private Store catalog could not be encoded")
		return
	}
	service.Run(ctx, payload)
}

func Register(ctx context.Context, issuer, proof string) {
	service.Register(ctx, issuer, proof)
}

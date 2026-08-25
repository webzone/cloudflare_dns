package service

import (
	"testing"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/store"
)

func TestZoneDiffAddsAndRemoves(t *testing.T) {
	cfZones := []cf.Zone{
		{ID: "a", Name: "a.com"}, // new: not in mirror
		{ID: "b", Name: "b.com"}, // existing
	}
	dbZones := map[string]store.Zone{
		"b": {ID: 2, Name: "b.com", ZoneID: "b", Status: store.StatusOn, Registrar: "cloudflare"},
		"c": {ID: 3, Name: "c.com", ZoneID: "c", Status: store.StatusOn, Registrar: "cloudflare"},  // vanished
		"d": {ID: 4, Name: "d.com", ZoneID: "d", Status: store.StatusOff, Registrar: "cloudflare"}, // already off: untouched
		"e": {ID: 5, Name: "e.com", ZoneID: "e", Status: store.StatusOn, Registrar: "enom"},        // not managed: untouched
	}
	added, removed := zoneDiff(cfZones, dbZones)
	if len(added) != 1 || added[0].ID != "a" || added[0].Name != "a.com" {
		t.Fatalf("want exactly a.com added, got %v", added)
	}
	if len(removed) != 1 || removed[0].Name != "c.com" {
		t.Fatalf("want exactly c.com removed, got %v", removed)
	}
}

func TestZoneDiffNoChanges(t *testing.T) {
	cfZones := []cf.Zone{{ID: "b", Name: "b.com"}}
	dbZones := map[string]store.Zone{
		"b": {ID: 2, Name: "b.com", ZoneID: "b", Status: store.StatusOn, Registrar: "cloudflare"},
	}
	added, removed := zoneDiff(cfZones, dbZones)
	if len(added) != 0 || len(removed) != 0 {
		t.Fatalf("want no changes, got added=%v removed=%v", added, removed)
	}
}

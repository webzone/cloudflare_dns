package service

import (
	"testing"

	"github.com/webzone/cloudflare_dns/internal/cf"
	"github.com/webzone/cloudflare_dns/internal/store"
)

func cfRecord(typ, name, content string) cf.Record {
	return cf.Record{Type: typ, Name: name, Content: content}
}

func TestMatchLegacyExact(t *testing.T) {
	rows := []store.Record{
		{ID: 1, Type: "A", Name: "*.example.com", Content: "1.2.3.4"},
	}
	if i := matchLegacy(rows, mirrorRecord(cfRecord("A", "*.example.com", "1.2.3.4")), "example.com"); i != 0 {
		t.Fatalf("exact triple should match, got %d", i)
	}
}

func TestMatchLegacyLabels(t *testing.T) {
	zone := "example.com"
	rec := cfRecord("A", "www.example.com", "1.2.3.4")

	cases := []struct {
		name     string
		rec      cf.Record
		row      store.Record
		want     int
	}{
		{"@ apex", cfRecord("A", zone, "1.2.3.4"), store.Record{ID: 1, Type: "A", Name: "@", Content: "1.2.3.4"}, 0},
		{"@ vs subdomain", rec, store.Record{ID: 2, Type: "A", Name: "@", Content: "1.2.3.4"}, -1},
		{"www literal", rec, store.Record{ID: 3, Type: "A", Name: "www", Content: "1.2.3.4"}, 0},
		{"* vs www", rec, store.Record{ID: 4, Type: "A", Name: "*", Content: "1.2.3.4"}, -1},
		{"wrong type", rec, store.Record{ID: 5, Type: "TXT", Name: "@", Content: "1.2.3.4"}, -1},
		{"wrong content", rec, store.Record{ID: 6, Type: "A", Name: "www", Content: "9.9.9.9"}, -1},
	}
	for _, tc := range cases {
		rows := []store.Record{tc.row}
		if got := matchLegacy(rows, mirrorRecord(tc.rec), zone); got != tc.want {
			t.Errorf("%s: want %d, got %d", tc.name, tc.want, got)
		}
	}
}

func TestMatchLegacyWildcard(t *testing.T) {
	zone := "example.com"
	rec := cfRecord("A", "*.example.com", "1.2.3.4")
	rows := []store.Record{{ID: 1, Type: "A", Name: "*", Content: "1.2.3.4"}}
	if got := matchLegacy(rows, mirrorRecord(rec), zone); got != 0 {
		t.Fatalf("wildcard label should match FQDN, got %d", got)
	}
}

func TestRecordEqual(t *testing.T) {
	a := store.Record{Type: "A", Name: "x.example.com", Content: "1.2.3.4", Proxied: true, TTL: 1}
	b := a
	if !recordEqual(a, b) {
		t.Fatal("identical records should be equal")
	}
	b.Content = "5.6.7.8"
	if recordEqual(a, b) {
		t.Fatal("content change should be detected")
	}
	b = a
	b.TTL = 3600
	if recordEqual(a, b) {
		t.Fatal("ttl change should be detected")
	}
	b = a
	b.Proxied = false
	if recordEqual(a, b) {
		t.Fatal("proxied change should be detected")
	}
}

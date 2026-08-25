package service

import (
	"testing"

	"github.com/webzone/cloudflare_dns/internal/store"
)

func TestSplitTracked(t *testing.T) {
	recs := []store.HomeRecord{
		{Record: store.Record{ID: 1, Type: "A", Name: "a.example.com", Content: "1.2.3.4"}},
		{Record: store.Record{ID: 2, Type: "A", Name: "b.example.com", Content: "1.2.3.4"}},
		{Record: store.Record{ID: 3, Type: "A", Name: "c.example.com", Content: "5.6.7.8"}},
	}
	pending, skipped := splitTracked(recs, "1.2.3.4")
	if skipped != 2 {
		t.Fatalf("want 2 skipped, got %d", skipped)
	}
	if len(pending) != 1 || pending[0].ID != 3 {
		t.Fatalf("want 1 pending record (ID 3), got %v", pending)
	}
}

func TestSplitTrackedAllSame(t *testing.T) {
	recs := []store.HomeRecord{
		{Record: store.Record{ID: 1, Content: "1.2.3.4"}},
		{Record: store.Record{ID: 2, Content: "1.2.3.4"}},
	}
	pending, skipped := splitTracked(recs, "1.2.3.4")
	if skipped != 2 || len(pending) != 0 {
		t.Fatalf("want all skipped, got skipped=%d pending=%d", skipped, len(pending))
	}
}

func TestSplitTrackedEmpty(t *testing.T) {
	pending, skipped := splitTracked(nil, "1.2.3.4")
	if skipped != 0 || len(pending) != 0 {
		t.Fatalf("empty input should yield nothing, got skipped=%d pending=%d", skipped, len(pending))
	}
}

func TestFQDNHost(t *testing.T) {
	cases := []struct {
		label, zone, want string
	}{
		{"@", "example.com", "example.com"},
		{"*", "example.com", "*.example.com"},
		{"www", "example.com", "www.example.com"},
		{"api", "example.com", "api.example.com"},
	}
	for _, tc := range cases {
		if got := FQDNHost(tc.label, tc.zone); got != tc.want {
			t.Errorf("FQDNHost(%q,%q) = %q, want %q", tc.label, tc.zone, got, tc.want)
		}
	}
}

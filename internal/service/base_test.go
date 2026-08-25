package service

import (
	"reflect"
	"testing"

	"github.com/webzone/cloudflare_dns/internal/cf"
)

func TestMissingBaseLabelsEmpty(t *testing.T) {
	if got := missingBaseLabels(nil, "example.com"); !reflect.DeepEqual(got, baseLabels) {
		t.Fatalf("empty zone: want all of %v, got %v", baseLabels, got)
	}
}

func TestMissingBaseLabelsComplete(t *testing.T) {
	existing := []cf.Record{
		{Type: "A", Name: "example.com"},
		{Type: "A", Name: "www.example.com"},
		{Type: "A", Name: "*.example.com"},
	}
	if got := missingBaseLabels(existing, "example.com"); len(got) != 0 {
		t.Fatalf("complete zone should need nothing, got %v", got)
	}
}

func TestMissingBaseLabelsPartial(t *testing.T) {
	existing := []cf.Record{
		{Type: "A", Name: "www.example.com"},
	}
	if got := missingBaseLabels(existing, "example.com"); !reflect.DeepEqual(got, []string{"@", "*"}) {
		t.Fatalf("want [@ *], got %v", got)
	}
}

func TestMissingBaseLabelsNonAOccupies(t *testing.T) {
	// A CNAME at www means no A is created there (CF forbids coexistence).
	existing := []cf.Record{
		{Type: "A", Name: "example.com"},
		{Type: "CNAME", Name: "www.example.com"},
	}
	if got := missingBaseLabels(existing, "example.com"); !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("CNAME should occupy www, want [*], got %v", got)
	}
}

func TestMissingBaseLabelsCaseInsensitive(t *testing.T) {
	existing := []cf.Record{
		{Type: "A", Name: "WWW.Example.COM"},
	}
	if got := missingBaseLabels(existing, "example.com"); !reflect.DeepEqual(got, []string{"@", "*"}) {
		t.Fatalf("case should not matter, want [@ *], got %v", got)
	}
}

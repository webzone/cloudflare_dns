package cli

import (
	"reflect"
	"testing"
)

func TestParseOptsSimple(t *testing.T) {
	rest, o, err := parseOpts([]string{"--dry-run", "dns", "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !o.dryRun {
		t.Fatal("dry-run should be set")
	}
	if !reflect.DeepEqual(rest, []string{"dns", "example.com"}) {
		t.Fatalf("rest = %v", rest)
	}
}

func TestParseOptsValues(t *testing.T) {
	rest, o, err := parseOpts([]string{"dns", "add", "example.com", "A", "www", "1.2.3.4",
		"--ttl", "60", "--no-proxy", "--prio", "10", "--type", "mx", "--content", "x y"})
	if err != nil {
		t.Fatal(err)
	}
	if o.ttl != 60 || !o.noProxy || o.prio != 10 || o.typ != "MX" || o.content != "x y" {
		t.Fatalf("opts = %+v", o)
	}
	wantRest := []string{"dns", "add", "example.com", "A", "www", "1.2.3.4"}
	if !reflect.DeepEqual(rest, wantRest) {
		t.Fatalf("rest = %v, want %v", rest, wantRest)
	}
}

func TestParseOptsMissingValue(t *testing.T) {
	if _, _, err := parseOpts([]string{"--ttl"}); err == nil {
		t.Fatal("--ttl without value should error")
	}
	if _, _, err := parseOpts([]string{"--prio", "abc"}); err == nil {
		t.Fatal("non-numeric --prio should error")
	}
}

func TestParseOptsYesAliases(t *testing.T) {
	for _, a := range []string{"-y", "--yes"} {
		_, o, err := parseOpts([]string{"dns", "rm", "example.com", "www", a})
		if err != nil {
			t.Fatal(err)
		}
		if !o.yes {
			t.Fatalf("%s should set yes", a)
		}
	}
}

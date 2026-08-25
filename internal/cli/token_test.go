package cli

import "testing"

func TestMaskToken(t *testing.T) {
	if maskToken("cfut_abcdefghij") != "cfut_a••••ghij" {
		t.Fatalf("long mask = %s", maskToken("cfut_abcdefghij"))
	}
	if maskToken("short") != "••••••" {
		t.Fatalf("short mask = %s", maskToken("short"))
	}
}

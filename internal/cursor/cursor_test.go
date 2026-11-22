package cursor

import "testing"

func TestParseRoundTrip(t *testing.T) {
	c, ok := Parse("1700000000000000_42")
	if !ok || c.Micro != 1700000000000000 || c.ID != 42 || c.String() != "1700000000000000_42" {
		t.Fatalf("Parse = %+v %v", c, ok)
	}
}

func TestParseRejectsBadCursors(t *testing.T) {
	for _, s := range []string{"", "abc", "1_", "_1", "-1_2", "1_-2", "1_2_3", "+1_2",
		"99999999999999999999_1", "9223372036854775807_1", "253402300800000000_1"} {
		if _, ok := Parse(s); ok {
			t.Errorf("Parse(%q) accepted", s)
		}
	}
	if _, ok := Parse("253402300799999999_1"); !ok {
		t.Error("latest valid cursor rejected")
	}
}

package money

import "testing"

func TestParse(t *testing.T) {
	cases := map[string]Amount{
		"0": 0, "89": 8900, "89.5": 8950, "89,50": 8950, "1 250.00": 125000, "1 250": 125000, "-3.10": -310,
	}
	for in, want := range cases {
		got, err := Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "abc", "1.234", "1.2.3", ".5", "99999999999999"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

func TestString(t *testing.T) {
	if got := Amount(125000).String(); got != "1 250.00" {
		t.Errorf("String = %q", got)
	}
	if got := Amount(-310).Plain(); got != "-3.10" {
		t.Errorf("Plain = %q", got)
	}
	if got := Amount(8900).MulRate(2.5); got != 22250 {
		t.Errorf("MulRate = %v", got)
	}
}

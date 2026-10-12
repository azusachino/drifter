package companion

import "testing"

func TestNormalizeISBN(t *testing.T) {
	for _, tt := range []struct{ input, want string }{
		{"978-7-5125-2382-1", "9787512523821"},
		{"0593716965", "9780593716960"},
		{"0-8044-2957-X", "9780804429573"},
		{"9798988837404", "9798988837404"},
	} {
		got, err := NormalizeISBN(tt.input)
		if err != nil || got != tt.want {
			t.Errorf("%q: got %q, %v; want %q", tt.input, got, err, tt.want)
		}
	}
	for _, input := range []string{"", "9787512523822", "B0768ZM5QH", "1234567890", "０５９３７１６９６５"} {
		if _, err := NormalizeISBN(input); err == nil {
			t.Errorf("accepted invalid identifier %q", input)
		}
	}
}

func TestProgressDoesNotInventUnreadOrCompletion(t *testing.T) {
	for _, tt := range []struct {
		percent, started int
		finished         int64
		want             string
	}{
		{0, 0, 0, "unknown"}, {0, 1, 0, "reading"}, {1, 0, 0, "reading"},
		{100, 1, 0, "unknown"}, {100, 1, 123, "read"},
	} {
		got, err := progressStatus(tt.percent, tt.started, tt.finished)
		if err != nil || got.Status != tt.want {
			t.Errorf("%+v: got %+v, %v", tt, got, err)
		}
	}
	for _, tt := range [][3]int{{101, 1, 0}, {-1, 0, 0}, {10, 2, 0}, {10, 1, -1}} {
		if _, err := progressStatus(tt[0], tt[1], int64(tt[2])); err == nil {
			t.Errorf("accepted %+v", tt)
		}
	}
}

func TestBookID(t *testing.T) {
	for _, id := range []string{"", "0", "00", "../12", "12\n", "-1"} {
		if ValidBookID(id) {
			t.Errorf("accepted %q", id)
		}
	}
	if !ValidBookID("3300228952") {
		t.Fatal("valid ID rejected")
	}
}

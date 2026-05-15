package main

import "testing"

func TestKindLabel(t *testing.T) {
	tests := []struct {
		kind int
		want string
	}{
		{10000, "mute"},
		{10001, "pin"},
		{10002, "relay"},
		{10003, "bookmark"},
		{10050, "dm-relay"},
		{30000, "follow-set"},
		{30030, "emoji-set"},
		{12345, "kind:12345"},
		{0, "kind:0"},
	}
	for _, tt := range tests {
		if got := kindLabel(tt.kind); got != tt.want {
			t.Errorf("kindLabel(%d): got=%q want=%q", tt.kind, got, tt.want)
		}
	}
}

func TestIsStandardList(t *testing.T) {
	cases := map[int]bool{
		9999:  false,
		10000: true,
		10003: true,
		19999: true,
		20000: false,
		30000: false,
		0:     false,
	}
	for k, want := range cases {
		if got := isStandardList(k); got != want {
			t.Errorf("isStandardList(%d): got=%v want=%v", k, got, want)
		}
	}
}

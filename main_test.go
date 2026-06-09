package main

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestIncludeKind(t *testing.T) {
	tests := []struct {
		name       string
		kinds      []int
		candidates []int
		want       bool
	}{
		{"empty kinds", nil, []int{1}, false},
		{"empty candidates", []int{1}, nil, false},
		{"hit single", []int{1}, []int{1}, true},
		{"hit among many candidates", []int{1}, []int{4, 1059, 1}, true},
		{"miss", []int{0, 3}, []int{1, 4}, false},
		{"first kind matches second candidate", []int{42}, []int{1, 42}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := includeKind(tt.kinds, tt.candidates...); got != tt.want {
				t.Errorf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestNormalizeProfileKey(t *testing.T) {
	pubkey := strings.Repeat("0", 64)
	npub, err := nip19.EncodePublicKey(pubkey)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		key  string
		want string
		ok   bool
	}{
		{"hex", pubkey, pubkey, true},
		{"npub", npub, pubkey, true},
		{"invalid", "not-a-profile-key", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeProfileKey(tt.key)
			if got != tt.want || ok != tt.ok {
				t.Errorf("got=(%q, %v) want=(%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestMissingProfilePubkeys(t *testing.T) {
	profiles := map[string]Profile{
		"a": {Name: "cached"},
	}
	follows := []string{"a", "b", "b", "c", "d"}

	got := missingProfilePubkeys(profiles, follows, 2)
	want := []string{"b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got=%v want=%v", got, want)
		}
	}
}

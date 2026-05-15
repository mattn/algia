package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr/nip19"
)

func TestResolveChannelID_Hex(t *testing.T) {
	const hex = "0000000000000000000000000000000000000000000000000000000000000001"
	got, err := resolveChannelID(hex)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != hex {
		t.Errorf("got=%q want=%q", got, hex)
	}
}

func TestResolveChannelID_Note(t *testing.T) {
	const hex = "0000000000000000000000000000000000000000000000000000000000000001"
	note, err := nip19.EncodeNote(hex)
	if err != nil {
		t.Fatalf("EncodeNote: %v", err)
	}
	got, err := resolveChannelID(note)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != hex {
		t.Errorf("got=%q want=%q", got, hex)
	}
}

func TestResolveChannelID_Nevent(t *testing.T) {
	const hex = "0000000000000000000000000000000000000000000000000000000000000002"
	const author = "1111111111111111111111111111111111111111111111111111111111111111"
	nev, err := nip19.EncodeEvent(hex, []string{"wss://example.com"}, author)
	if err != nil {
		t.Fatalf("EncodeEvent: %v", err)
	}
	got, err := resolveChannelID(nev)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if got != hex {
		t.Errorf("got=%q want=%q", got, hex)
	}
}

func TestResolveChannelID_Invalid(t *testing.T) {
	if _, err := resolveChannelID("not-a-valid-id"); err == nil {
		t.Fatal("expected error for invalid id")
	}
}

func TestChannelMetadata_JSONRoundTrip(t *testing.T) {
	m := channelMetadata{
		Name:    "test",
		About:   "for testing",
		Picture: "https://example.com/p.png",
		Relays:  []string{"wss://a", "wss://b"},
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, want := range []string{`"name":"test"`, `"about":"for testing"`, `"picture":"https://example.com/p.png"`, `"relays":["wss://a","wss://b"]`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in %s", want, s)
		}
	}

	var back channelMetadata
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Name != m.Name || back.About != m.About || back.Picture != m.Picture || len(back.Relays) != 2 {
		t.Errorf("round-trip mismatch: %+v", back)
	}
}

func TestChannelMetadata_OmitEmpty(t *testing.T) {
	b, err := json.Marshal(channelMetadata{Name: "only"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if strings.Contains(s, "about") || strings.Contains(s, "picture") || strings.Contains(s, "relays") {
		t.Errorf("expected omitempty fields absent, got %s", s)
	}
}

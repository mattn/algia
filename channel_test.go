package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func findTag(tags nostr.Tags, name string) nostr.Tag {
	for _, t := range tags {
		if len(t) > 0 && t[0] == name {
			return t
		}
	}
	return nil
}

func findAllTags(tags nostr.Tags, name string) nostr.Tags {
	var out nostr.Tags
	for _, t := range tags {
		if len(t) > 0 && t[0] == name {
			out = append(out, t)
		}
	}
	return out
}

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

func TestBuildChannelCreateEvent(t *testing.T) {
	const pub = "abcdef0000000000000000000000000000000000000000000000000000000001"
	ev, err := buildChannelCreateEvent(pub, channelMetadata{Name: "room", About: "for testing"}, 1700000000)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindChannelCreation {
		t.Errorf("kind: got=%d want=%d", ev.Kind, nostr.KindChannelCreation)
	}
	if ev.PubKey != pub {
		t.Errorf("pubkey mismatch")
	}
	if ev.CreatedAt != 1700000000 {
		t.Errorf("createdAt: got=%d want=%d", ev.CreatedAt, nostr.Timestamp(1700000000))
	}
	if !strings.Contains(ev.Content, `"name":"room"`) || !strings.Contains(ev.Content, `"about":"for testing"`) {
		t.Errorf("content: %s", ev.Content)
	}
	if findTag(ev.Tags, "client") == nil {
		t.Errorf("expected client tag")
	}
}

func TestBuildChannelCreateEvent_RejectsEmptyName(t *testing.T) {
	if _, err := buildChannelCreateEvent("pub", channelMetadata{Name: "   "}, 0); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestBuildChannelPostEvent_Basic(t *testing.T) {
	const pub = "abcdef0000000000000000000000000000000000000000000000000000000001"
	const chID = "1111111111111111111111111111111111111111111111111111111111111111"
	ev, err := buildChannelPostEvent(pub, "hello world", chID, "", "wss://example.com", 1700000000)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindChannelMessage {
		t.Errorf("kind: got=%d want=%d", ev.Kind, nostr.KindChannelMessage)
	}
	if ev.Content != "hello world" {
		t.Errorf("content mismatch")
	}
	etag := findTag(ev.Tags, "e")
	if etag == nil || len(etag) < 4 {
		t.Fatalf("missing e tag: %v", ev.Tags)
	}
	if etag[1] != chID || etag[2] != "wss://example.com" || etag[3] != "root" {
		t.Errorf("e tag wrong: %v", etag)
	}
	if len(findAllTags(ev.Tags, "e")) != 1 {
		t.Errorf("expected exactly one e tag without reply, got %v", findAllTags(ev.Tags, "e"))
	}
}

func TestBuildChannelPostEvent_WithReply(t *testing.T) {
	const chID = "1111111111111111111111111111111111111111111111111111111111111111"
	const rID = "2222222222222222222222222222222222222222222222222222222222222222"
	ev, err := buildChannelPostEvent("p", "hi", chID, rID, "", 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	es := findAllTags(ev.Tags, "e")
	if len(es) != 2 {
		t.Fatalf("expected 2 e tags, got %v", es)
	}
	if es[0][3] != "root" || es[0][1] != chID {
		t.Errorf("first e tag should be root channel: %v", es[0])
	}
	if es[1][3] != "reply" || es[1][1] != rID {
		t.Errorf("second e tag should be reply: %v", es[1])
	}
}

func TestBuildChannelPostEvent_ExtractsLinksAndTags(t *testing.T) {
	ev, err := buildChannelPostEvent("p", "check https://example.com and topic #nostr", "ch", "", "", 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	rs := findAllTags(ev.Tags, "r")
	if len(rs) != 1 || rs[0][1] != "https://example.com" {
		t.Errorf("expected r tag for URL, got %v", rs)
	}
	tt := findTag(ev.Tags, "t")
	if tt == nil || len(tt) < 2 || tt[1] != "nostr" {
		t.Errorf("expected t tag with 'nostr', got %v", tt)
	}
}

func TestBuildChannelPostEvent_RejectsEmptyContent(t *testing.T) {
	if _, err := buildChannelPostEvent("p", "   \n", "ch", "", "", 0); err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestBuildChannelPostEvent_RejectsEmptyChannelID(t *testing.T) {
	if _, err := buildChannelPostEvent("p", "hi", "", "", "", 0); err == nil {
		t.Fatal("expected error for empty channel id")
	}
}

func TestFirstWriteRelay(t *testing.T) {
	cfg := &Config{Relays: map[string]Relay{
		"wss://b.example": {Write: true},
		"wss://a.example": {Write: true},
		"wss://r-only":    {Read: true},
	}}
	if got := firstWriteRelay(cfg); got != "wss://a.example" {
		t.Errorf("got=%q want=wss://a.example", got)
	}

	empty := &Config{Relays: map[string]Relay{"wss://r-only": {Read: true}}}
	if got := firstWriteRelay(empty); got != "" {
		t.Errorf("expected empty, got %q", got)
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

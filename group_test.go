package main

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestParseGroupMetadata(t *testing.T) {
	ev := &nostr.Event{
		Kind: nostr.KindSimpleGroupMetadata,
		Tags: nostr.Tags{
			nostr.Tag{"d", "abc-123"},
			nostr.Tag{"name", "general"},
			nostr.Tag{"about", "General chat"},
			nostr.Tag{"private"},
			nostr.Tag{"closed"},
			nostr.Tag{"t", "stream"},
		},
	}
	g := parseGroupMetadata(ev)
	if g.ID != "abc-123" {
		t.Errorf("ID=%q want %q", g.ID, "abc-123")
	}
	if g.Name != "general" {
		t.Errorf("Name=%q want %q", g.Name, "general")
	}
	if g.About != "General chat" {
		t.Errorf("About=%q want %q", g.About, "General chat")
	}
	if g.Type != "stream" {
		t.Errorf("Type=%q want %q", g.Type, "stream")
	}
	if !g.Private {
		t.Errorf("Private=false want true")
	}
	if !g.Closed {
		t.Errorf("Closed=false want true")
	}
}

func TestBuildGroupPostEvent(t *testing.T) {
	const pub = "0000000000000000000000000000000000000000000000000000000000000001"
	ev, err := buildGroupPostEvent(pub, "grp-42", "hello #nostr https://example.com", 12345)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindSimpleGroupChatMessage {
		t.Errorf("Kind=%d want %d", ev.Kind, nostr.KindSimpleGroupChatMessage)
	}
	h := findTag(ev.Tags, "h")
	if h == nil || len(h) < 2 || h[1] != "grp-42" {
		t.Errorf("h tag=%v want [h grp-42]", h)
	}
	if r := findTag(ev.Tags, "r"); r == nil || r[1] != "https://example.com" {
		t.Errorf("r tag=%v want link", r)
	}
	if tg := findTag(ev.Tags, "t"); tg == nil || len(tg) < 2 || tg[1] != "nostr" {
		t.Errorf("t tag=%v want hashtag nostr", tg)
	}
}

func TestBuildGroupPostEvent_Errors(t *testing.T) {
	const pub = "0000000000000000000000000000000000000000000000000000000000000001"
	if _, err := buildGroupPostEvent(pub, "grp", "   ", 1); err == nil {
		t.Errorf("empty content: want error")
	}
	if _, err := buildGroupPostEvent(pub, "", "hi", 1); err == nil {
		t.Errorf("empty group id: want error")
	}
}

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
	ev, err := buildGroupPostEvent(pub, "grp-42", "hello #nostr https://example.com", "", 12345)
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

func TestBuildGroupDeleteEvent(t *testing.T) {
	const pub = "0000000000000000000000000000000000000000000000000000000000000001"
	ev, err := buildGroupDeleteEvent(pub, "grp-42", "aa", 999)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindSimpleGroupDeleteEvent {
		t.Errorf("Kind=%d want %d", ev.Kind, nostr.KindSimpleGroupDeleteEvent)
	}
	if h := findTag(ev.Tags, "h"); h == nil || len(h) < 2 || h[1] != "grp-42" {
		t.Errorf("h tag=%v want [h grp-42]", h)
	}
	// A 9005 must reference exactly one target.
	es := findAllTags(ev.Tags, "e")
	if len(es) != 1 || es[0][1] != "aa" {
		t.Errorf("e tags=%v want single target aa", es)
	}
	if _, err := buildGroupDeleteEvent(pub, "grp", "", 1); err == nil {
		t.Errorf("no target: want error")
	}
	if _, err := buildGroupDeleteEvent(pub, "", "aa", 1); err == nil {
		t.Errorf("empty group id: want error")
	}
}

func TestBuildGroupReactEvent(t *testing.T) {
	const pub = "0000000000000000000000000000000000000000000000000000000000000001"
	ev, err := buildGroupReactEvent(pub, "grp-42", "target-id", "", "", 7)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindReaction {
		t.Errorf("Kind=%d want %d", ev.Kind, nostr.KindReaction)
	}
	if ev.Content != "+" {
		t.Errorf("Content=%q want default +", ev.Content)
	}
	if h := findTag(ev.Tags, "h"); h == nil || len(h) < 2 || h[1] != "grp-42" {
		t.Errorf("h tag=%v want [h grp-42]", h)
	}
	if e := findTag(ev.Tags, "e"); e == nil || len(e) < 2 || e[1] != "target-id" {
		t.Errorf("e tag=%v want [e target-id]", e)
	}

	ev, err = buildGroupReactEvent(pub, "grp", "t", "star", "https://ex/star.png", 7)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Content != ":star:" {
		t.Errorf("Content=%q want :star:", ev.Content)
	}
	if em := findTag(ev.Tags, "emoji"); em == nil || em[1] != "star" || em[2] != "https://ex/star.png" {
		t.Errorf("emoji tag=%v", em)
	}

	if _, err := buildGroupReactEvent(pub, "", "t", "+", "", 7); err == nil {
		t.Errorf("empty group id: want error")
	}
	if _, err := buildGroupReactEvent(pub, "g", "", "+", "", 7); err == nil {
		t.Errorf("empty target id: want error")
	}
}

func TestBuildGroupJoinEvent(t *testing.T) {
	const pub = "0000000000000000000000000000000000000000000000000000000000000001"
	ev, err := buildGroupJoinEvent(pub, "grp-42", "", 9)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindSimpleGroupJoinRequest {
		t.Errorf("Kind=%d want %d", ev.Kind, nostr.KindSimpleGroupJoinRequest)
	}
	if h := findTag(ev.Tags, "h"); h == nil || len(h) < 2 || h[1] != "grp-42" {
		t.Errorf("h tag=%v want [h grp-42]", h)
	}
	if c := findTag(ev.Tags, "code"); c != nil {
		t.Errorf("code tag=%v want none", c)
	}

	ev, err = buildGroupJoinEvent(pub, "grp", "invite123", 9)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if c := findTag(ev.Tags, "code"); c == nil || len(c) < 2 || c[1] != "invite123" {
		t.Errorf("code tag=%v want [code invite123]", c)
	}

	if _, err := buildGroupJoinEvent(pub, "", "", 9); err == nil {
		t.Errorf("empty group id: want error")
	}
}

func TestBuildGroupLeaveEvent(t *testing.T) {
	const pub = "0000000000000000000000000000000000000000000000000000000000000001"
	ev, err := buildGroupLeaveEvent(pub, "grp-42", 9)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindSimpleGroupLeaveRequest {
		t.Errorf("Kind=%d want %d", ev.Kind, nostr.KindSimpleGroupLeaveRequest)
	}
	if h := findTag(ev.Tags, "h"); h == nil || len(h) < 2 || h[1] != "grp-42" {
		t.Errorf("h tag=%v want [h grp-42]", h)
	}
	if _, err := buildGroupLeaveEvent(pub, "", 9); err == nil {
		t.Errorf("empty group id: want error")
	}
}

func TestResolveGroupRef_Passthrough(t *testing.T) {
	// A non-"#" reference (a UUID / h-tag) is returned unchanged without any
	// relay lookup, so a nil context is safe here.
	const uuid = "fe96b435-7e47-4794-b3aa-9392fb2243f1"
	got, err := resolveGroupRef(nil, uuid)
	if err != nil || got != uuid {
		t.Errorf("resolveGroupRef(%q)=(%q,%v) want (%q,nil)", uuid, got, err, uuid)
	}
}

func TestBuildGroupPostEvent_Reply(t *testing.T) {
	const pub = "0000000000000000000000000000000000000000000000000000000000000001"
	ev, err := buildGroupPostEvent(pub, "grp-42", "ｶﾞｯ", "targetid", 12345)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	e := findTag(ev.Tags, "e")
	// Buzz recognizes a reply only with the NIP-10 "reply" marker.
	want := nostr.Tag{"e", "targetid", "", "reply"}
	if e == nil || len(e) != len(want) {
		t.Fatalf("e tag=%v want %v", e, want)
	}
	for i := range want {
		if e[i] != want[i] {
			t.Errorf("e[%d]=%q want %q", i, e[i], want[i])
		}
	}
}

func TestBuildGroupPostEvent_Errors(t *testing.T) {
	const pub = "0000000000000000000000000000000000000000000000000000000000000001"
	if _, err := buildGroupPostEvent(pub, "grp", "   ", "", 1); err == nil {
		t.Errorf("empty content: want error")
	}
	if _, err := buildGroupPostEvent(pub, "", "hi", "", 1); err == nil {
		t.Errorf("empty group id: want error")
	}
}

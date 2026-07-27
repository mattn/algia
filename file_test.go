package main

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nipb0/blossom"
)

func TestRelayHTTPBase(t *testing.T) {
	cases := map[string]string{
		"wss://relay.example":       "https://relay.example",
		"ws://relay.example:7000":   "http://relay.example:7000",
		"https://relay.example/med": "https://relay.example/med",
	}
	for in, want := range cases {
		if got := relayHTTPBase(in); got != want {
			t.Errorf("relayHTTPBase(%q)=%q want %q", in, got, want)
		}
	}
}

func TestAppendImageURLs(t *testing.T) {
	bds := []*blossom.BlobDescriptor{
		{URL: "https://s/a.png"},
		{URL: ""}, // skipped
		{URL: "https://s/b.jpg"},
	}
	if got := appendImageURLs("hello", bds); got != "hello\nhttps://s/a.png\nhttps://s/b.jpg" {
		t.Errorf("with text: got %q", got)
	}
	if got := appendImageURLs("", bds); got != "https://s/a.png\nhttps://s/b.jpg" {
		t.Errorf("image-only: got %q", got)
	}
	if got := appendImageURLs("x", nil); got != "x" {
		t.Errorf("no images: got %q", got)
	}
}

func TestAddImetaTags(t *testing.T) {
	ev := &nostr.Event{}
	addImetaTags(ev, []*blossom.BlobDescriptor{
		{URL: "https://s/a.png", Type: "image/png", SHA256: "abc", Size: 12},
		{URL: ""}, // skipped
	})
	tags := findAllTags(ev.Tags, "imeta")
	if len(tags) != 1 {
		t.Fatalf("imeta tags=%d want 1", len(tags))
	}
	got := tags[0]
	want := nostr.Tag{"imeta", "url https://s/a.png", "m image/png", "x abc", "size 12"}
	if len(got) != len(want) {
		t.Fatalf("imeta=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("imeta[%d]=%q want %q", i, got[i], want[i])
		}
	}
}

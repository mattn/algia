package main

import (
	"reflect"
	"testing"
)

func texts(es []entry) []string {
	if len(es) == 0 {
		return nil
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.text)
	}
	return out
}

func TestExtractLinks(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "no urls here", nil},
		{"http", "see http://example.com", []string{"http://example.com"}},
		{"https with path", "https://example.com/a/b?x=1", []string{"https://example.com/a/b?x=1"}},
		{"multiple", "a https://a.example b http://b.example c", []string{"https://a.example", "http://b.example"}},
		{"japanese around", "URLは https://example.com です", []string{"https://example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := texts(extractLinks(tt.in))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestExtractMentions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "no mention here", nil},
		{"simple", "hello @alice", []string{"alice"}},
		{"multiple", "@alice and @bob.jp", []string{"alice", "bob.jp"}},
		{"dot allowed", "@foo.bar.baz", []string{"foo.bar.baz"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := texts(extractMentions(tt.in))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestExtractEmojis(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "no emoji here", nil},
		{"one", "hello :wave: world", []string{":wave:"}},
		{"two", "a :smile: b :heart: c", []string{":smile:", ":heart:"}},
		{"alnum only", "::: :a1b2:", []string{":a1b2:"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := texts(extractEmojis(tt.in))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestExtractTags(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"none", "no tag here", nil},
		{"one", "talk about #nostr today", []string{"nostr"}},
		{"multiple", "#foo and #bar", []string{"foo", "bar"}},
		{"japanese", "今日は #寿司 を食べた", []string{"寿司"}},
		{"after word char does not match (\\B fails)", "see example.com#frag", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := texts(extractTags(tt.in))
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got=%v want=%v", got, tt.want)
			}
		})
	}
}

func TestExtractRuneOffsets(t *testing.T) {
	in := "あいう https://example.com えお"
	got := extractLinks(in)
	if len(got) != 1 {
		t.Fatalf("expected 1 link, got %d", len(got))
	}
	if got[0].start != 4 {
		t.Fatalf("start: got=%d want=4 (rune index, not byte)", got[0].start)
	}
	wantEnd := int64(4 + len("https://example.com"))
	if got[0].end != wantEnd {
		t.Fatalf("end: got=%d want=%d", got[0].end, wantEnd)
	}
}

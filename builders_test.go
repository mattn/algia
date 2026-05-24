package main

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const testPub = "abcdef0000000000000000000000000000000000000000000000000000000001"
const testTargetID = "1111111111111111111111111111111111111111111111111111111111111111"

// --- buildDeleteEvent ---

func TestBuildDeleteEvent(t *testing.T) {
	ev, err := buildDeleteEvent(testPub, testTargetID, 0, 100)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindDeletion {
		t.Errorf("kind: got=%d want=%d", ev.Kind, nostr.KindDeletion)
	}
	if ev.PubKey != testPub {
		t.Errorf("pubkey mismatch")
	}
	if ev.CreatedAt != 100 {
		t.Errorf("createdAt: got=%d want=100", ev.CreatedAt)
	}
	e := findTag(ev.Tags, "e")
	if e == nil || len(e) < 2 || e[1] != testTargetID {
		t.Errorf("missing/wrong e tag: %v", ev.Tags)
	}
	if k := findTag(ev.Tags, "k"); k != nil {
		t.Errorf("unexpected k tag for targetKind=0: %v", k)
	}
}

func TestBuildDeleteEvent_WithKind(t *testing.T) {
	ev, err := buildDeleteEvent(testPub, testTargetID, nostr.KindTextNote, 100)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	k := findTag(ev.Tags, "k")
	if k == nil || len(k) < 2 || k[1] != "1" {
		t.Errorf("missing/wrong k tag: %v", ev.Tags)
	}
}

func TestBuildDeleteEvent_EmptyID(t *testing.T) {
	if _, err := buildDeleteEvent(testPub, "", 0, 0); err == nil {
		t.Fatal("expected error")
	}
}

// --- buildLikeEvent ---

func TestBuildLikeEvent_DefaultsToPlus(t *testing.T) {
	ev, err := buildLikeEvent(testPub, testTargetID, "", "", "", nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindReaction {
		t.Errorf("kind: got=%d want=%d", ev.Kind, nostr.KindReaction)
	}
	if ev.Content != "+" {
		t.Errorf("default content: got=%q want=+", ev.Content)
	}
}

func TestBuildLikeEvent_ExplicitContent(t *testing.T) {
	ev, err := buildLikeEvent(testPub, testTargetID, "", "🔥", "", nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Content != "🔥" {
		t.Errorf("content: got=%q want=🔥", ev.Content)
	}
}

func TestBuildLikeEvent_EmojiWrapsContent(t *testing.T) {
	ev, err := buildLikeEvent(testPub, testTargetID, "", "love", "https://e.example/love.png", nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Content != ":love:" {
		t.Errorf("content: got=%q want=:love:", ev.Content)
	}
	em := findTag(ev.Tags, "emoji")
	if em == nil || len(em) < 3 || em[1] != "love" || em[2] != "https://e.example/love.png" {
		t.Errorf("emoji tag wrong: %v", em)
	}
}

func TestBuildLikeEvent_EmojiWithoutContent(t *testing.T) {
	ev, err := buildLikeEvent(testPub, testTargetID, "", "", "https://e.example/x.png", nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Content != ":like:" {
		t.Errorf("expected default :like:, got %q", ev.Content)
	}
}

func TestBuildLikeEvent_MentionedPubkeys(t *testing.T) {
	pubs := []string{"aa", "bb"}
	ev, err := buildLikeEvent(testPub, testTargetID, "", "", "", pubs, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	ps := findAllTags(ev.Tags, "p")
	if len(ps) != 2 || ps[0][1] != "aa" || ps[1][1] != "bb" {
		t.Errorf("p tags wrong: %v", ps)
	}
}

func TestBuildLikeEvent_RelayHint(t *testing.T) {
	ev, err := buildLikeEvent(testPub, testTargetID, "wss://r.example", "", "", nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	e := findTag(ev.Tags, "e")
	if e == nil || len(e) < 3 || e[1] != testTargetID || e[2] != "wss://r.example" {
		t.Errorf("e tag wrong: %v", e)
	}
}

// --- buildRepostEvent ---

func TestBuildRepostEvent_Basic(t *testing.T) {
	ev, err := buildRepostEvent(testPub, testTargetID, "wss://r.example", nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindRepost {
		t.Errorf("kind: got=%d want=%d", ev.Kind, nostr.KindRepost)
	}
	if ev.Content != "" {
		t.Errorf("content should be empty, got %q", ev.Content)
	}
	e := findTag(ev.Tags, "e")
	if e == nil || len(e) < 3 || e[1] != testTargetID || e[2] != "wss://r.example" {
		t.Errorf("e tag wrong: %v", e)
	}
}

func TestBuildRepostEvent_NoRelayHint(t *testing.T) {
	ev, err := buildRepostEvent(testPub, testTargetID, "", nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	e := findTag(ev.Tags, "e")
	if e == nil || len(e) != 2 || e[1] != testTargetID {
		t.Errorf("e tag should have no relay hint when empty: %v", e)
	}
}

// --- buildPostEvent ---

func newPostArg(content string) *postArg {
	return &postArg{
		cfg:     &Config{Emojis: map[string]string{}},
		content: content,
	}
}

func TestBuildPostEvent_RejectsEmpty(t *testing.T) {
	if _, err := buildPostEvent(newPostArg("   "), testPub, nil, 0); err == nil {
		t.Fatal("expected error for blank content")
	}
}

func TestBuildPostEvent_TextNote(t *testing.T) {
	arg := newPostArg("hello world")
	ev, err := buildPostEvent(arg, testPub, nil, 1700000000)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindTextNote {
		t.Errorf("kind: got=%d want=%d", ev.Kind, nostr.KindTextNote)
	}
	if ev.Content != "hello world" {
		t.Errorf("content mismatch")
	}
	if findTag(ev.Tags, "client") == nil {
		t.Error("expected client tag")
	}
}

func TestBuildPostEvent_ExtractsLinksAndTags(t *testing.T) {
	arg := newPostArg("see https://example.com and topic #nostr")
	ev, err := buildPostEvent(arg, testPub, nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	rs := findAllTags(ev.Tags, "r")
	if len(rs) != 1 || rs[0][1] != "https://example.com" {
		t.Errorf("r tag: %v", rs)
	}
	tt := findTag(ev.Tags, "t")
	if tt == nil || len(tt) < 2 || tt[1] != "nostr" {
		t.Errorf("t tag: %v", tt)
	}
}

func TestBuildPostEvent_EmojiFlagAndInline(t *testing.T) {
	arg := newPostArg("hi :heart: there :wave: now")
	arg.cfg.Emojis = map[string]string{"heart": "https://e.example/h.png"}
	arg.emoji = []string{"wave=https://e.example/w.png"}
	ev, err := buildPostEvent(arg, testPub, nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	emojis := findAllTags(ev.Tags, "emoji")
	names := map[string]string{}
	for _, e := range emojis {
		if len(e) >= 3 {
			names[e[1]] = e[2]
		}
	}
	if names["heart"] != "https://e.example/h.png" {
		t.Errorf("missing inline emoji heart: %v", names)
	}
	if names["wave"] != "https://e.example/w.png" {
		t.Errorf("missing flag emoji wave: %v", names)
	}
}

func TestBuildPostEvent_EmojiBadFormat(t *testing.T) {
	arg := newPostArg("hi")
	arg.emoji = []string{"bad-no-equals"}
	if _, err := buildPostEvent(arg, testPub, nil, 0); err == nil {
		t.Fatal("expected error for malformed --emoji")
	}
}

func TestBuildPostEvent_Mentions(t *testing.T) {
	pub2 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	arg := newPostArg("ping")
	ev, err := buildPostEvent(arg, testPub, []string{pub2}, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if findTag(ev.Tags, "p") == nil {
		t.Error("expected p tag")
	}
	wantNpub, _ := nip19.EncodePublicKey(pub2)
	if !strings.HasPrefix(ev.Content, "nostr:"+wantNpub+" ") {
		t.Errorf("content should be prefixed with nostr:<npub>, got %q", ev.Content)
	}
}

func TestBuildPostEvent_SensitiveAndGeohash(t *testing.T) {
	arg := newPostArg("hi")
	arg.sensitive = "nsfw"
	arg.geohash = "xn76urwe"
	ev, err := buildPostEvent(arg, testPub, nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cw := findTag(ev.Tags, "content-warning"); cw == nil || cw[1] != "nsfw" {
		t.Errorf("content-warning: %v", cw)
	}
	if g := findTag(ev.Tags, "g"); g == nil || g[1] != "xn76urwe" {
		t.Errorf("g tag: %v", g)
	}
}

func TestBuildPostEvent_CustomTags(t *testing.T) {
	arg := newPostArg("hi")
	arg.tags = []string{"foo=bar;baz", "z"}
	ev, err := buildPostEvent(arg, testPub, nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	foo := findTag(ev.Tags, "foo")
	if foo == nil || len(foo) != 3 || foo[1] != "bar" || foo[2] != "baz" {
		t.Errorf("foo tag: %v", foo)
	}
	if findTag(ev.Tags, "z") == nil {
		t.Errorf("expected single-element tag z")
	}
}

func TestBuildPostEvent_CustomClientTagOverrides(t *testing.T) {
	arg := newPostArg("hi")
	arg.tags = []string{"client=foo;extra"}
	ev, err := buildPostEvent(arg, testPub, nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	clients := findAllTags(ev.Tags, "client")
	if len(clients) != 1 {
		t.Fatalf("expected exactly one client tag, got %d (%v)", len(clients), clients)
	}
	if clients[0][1] != "foo" {
		t.Errorf("client tag value: got=%q want=foo", clients[0][1])
	}
}

func TestBuildPostEvent_Article(t *testing.T) {
	arg := newPostArg("body of article")
	arg.articleName = "my-article"
	arg.articleTitle = "Hello"
	arg.articleSummary = "summary"
	ev, err := buildPostEvent(arg, testPub, nil, 1700000000)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindArticle {
		t.Errorf("kind: got=%d want=%d", ev.Kind, nostr.KindArticle)
	}
	if d := findTag(ev.Tags, "d"); d == nil || d[1] != "my-article" {
		t.Errorf("d tag: %v", d)
	}
	if tt := findTag(ev.Tags, "title"); tt == nil || tt[1] != "Hello" {
		t.Errorf("title tag: %v", tt)
	}
	if s := findTag(ev.Tags, "summary"); s == nil || s[1] != "summary" {
		t.Errorf("summary tag: %v", s)
	}
	if p := findTag(ev.Tags, "published_at"); p == nil || p[1] != "1700000000" {
		t.Errorf("published_at: %v", p)
	}
}

func TestBuildPostEvent_ArticleRequiresTitle(t *testing.T) {
	arg := newPostArg("body")
	arg.articleName = "x"
	if _, err := buildPostEvent(arg, testPub, nil, 0); err == nil {
		t.Fatal("expected error when article-name is set without article-title")
	}
}

// --- buildReplyEvent ---

func TestBuildReplyEvent_Basic(t *testing.T) {
	ev, err := buildReplyEvent(testPub, replyOpts{
		Content:   "thanks!",
		ReplyToID: testTargetID,
		RelayHint: "wss://r.example",
	}, nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if ev.Kind != nostr.KindTextNote {
		t.Errorf("kind: got=%d want=%d", ev.Kind, nostr.KindTextNote)
	}
	e := findTag(ev.Tags, "e")
	if e == nil || len(e) < 4 || e[1] != testTargetID || e[2] != "wss://r.example" || e[3] != "reply" {
		t.Errorf("e tag wrong: %v", e)
	}
}

func TestBuildReplyEvent_Quote(t *testing.T) {
	ev, err := buildReplyEvent(testPub, replyOpts{
		Content:   "quoting",
		ReplyToID: testTargetID,
		Quote:     true,
	}, nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	e := findTag(ev.Tags, "e")
	if e == nil || len(e) < 4 || e[3] != "mention" {
		t.Errorf("expected mention marker, got %v", e)
	}
}

func TestBuildReplyEvent_RejectsEmptyContent(t *testing.T) {
	if _, err := buildReplyEvent(testPub, replyOpts{Content: "  ", ReplyToID: testTargetID}, nil, 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildReplyEvent_RejectsEmptyTarget(t *testing.T) {
	if _, err := buildReplyEvent(testPub, replyOpts{Content: "hi"}, nil, 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestBuildReplyEvent_EmojiAndHashtag(t *testing.T) {
	ev, err := buildReplyEvent(testPub, replyOpts{
		Content:   "hi :wave: with #tag",
		ReplyToID: testTargetID,
		Emojis:    []string{"wave=https://e.example/w.png"},
	}, map[string]string{"wave": "ignored-because-flag-wins"}, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	// The flag adds an emoji tag first, AppendUnique should keep just the first.
	emojis := findAllTags(ev.Tags, "emoji")
	if len(emojis) != 1 {
		t.Errorf("expected 1 emoji tag (AppendUnique), got %v", emojis)
	}
	tt := findTag(ev.Tags, "t")
	if tt == nil || len(tt) < 2 || tt[1] != "tag" {
		t.Errorf("t tag: %v", tt)
	}
}

func TestBuildReplyEvent_SensitiveAndGeohash(t *testing.T) {
	ev, err := buildReplyEvent(testPub, replyOpts{
		Content:   "hi",
		ReplyToID: testTargetID,
		Sensitive: "nsfw",
		Geohash:   "xn76",
	}, nil, 0)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if cw := findTag(ev.Tags, "content-warning"); cw == nil || cw[1] != "nsfw" {
		t.Errorf("content-warning: %v", cw)
	}
	if g := findTag(ev.Tags, "g"); g == nil || g[1] != "xn76" {
		t.Errorf("g tag: %v", g)
	}
}

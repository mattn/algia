package main

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip49"
)

func TestIsRemoteSigner(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want bool
	}{
		{"bunker://" + testDelegatorPub + "?relay=wss://relay.example.com", true},
		{"alice@example.com", true},
		{"example.com", true},
		{"nsec1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq", false},
		{"", false},
	} {
		if got := isRemoteSigner(tt.in); got != tt.want {
			t.Errorf("isRemoteSigner(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestBunkerSessionKeyStripsSecret(t *testing.T) {
	a := bunkerSessionKey("bunker://" + testDelegatorPub + "?relay=wss://r.example.com&secret=abc")
	b := bunkerSessionKey("bunker://" + testDelegatorPub + "?relay=wss://r.example.com")
	if a != b {
		t.Fatalf("session key should ignore secret: %q != %q", a, b)
	}
}

func TestParseBunkerInput(t *testing.T) {
	in := "bunker://" + testDelegatorPub + "?relay=wss://a.example.com&relay=wss://b.example.com/&secret=s3"
	remote, relays, secret, err := parseBunkerInput(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if remote != testDelegatorPub || secret != "s3" || len(relays) != 2 {
		t.Fatalf("got remote=%s relays=%v secret=%s", remote, relays, secret)
	}
	if _, _, _, err := parseBunkerInput(context.Background(), "bunker://"+testDelegatorPub); err == nil {
		t.Fatal("expected error for bunker URL without relays")
	}
	if _, _, _, err := parseBunkerInput(context.Background(), "bunker://nothex?relay=wss://a.example.com"); err == nil {
		t.Fatal("expected error for invalid remote pubkey")
	}
}

func TestBunkerSessionRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	in := "bunker://" + testDelegatorPub + "?relay=wss://r.example.com&secret=x"
	if s := loadBunkerSession("p", in); s != nil {
		t.Fatalf("unexpected session %+v", s)
	}
	want := &bunkerSession{Bunker: bunkerSessionKey(in), ClientKey: nostr.GeneratePrivateKey(), PubKey: testDelegateePub}
	if err := saveBunkerSession("p", want); err != nil {
		t.Fatal(err)
	}
	got := loadBunkerSession("p", in)
	if got == nil || *got != *want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	// A different signer must not pick up this session.
	other := "bunker://" + testDelegateePub + "?relay=wss://r.example.com"
	if s := loadBunkerSession("p", other); s != nil {
		t.Fatalf("session leaked to another signer: %+v", s)
	}

	// pubkey() uses the stored session without contacting the signer.
	cfg := &Config{PrivateKey: in, profileName: "p"}
	pub, err := cfg.pubkey()
	if err != nil || pub != testDelegateePub {
		t.Fatalf("pubkey() = %q, %v", pub, err)
	}
	if _, err := cfg.localSecretKey(); err != errNeedLocalKey {
		t.Fatalf("localSecretKey() err = %v, want errNeedLocalKey", err)
	}
}

func TestLocalSigner(t *testing.T) {
	nsec, _ := nip19.EncodePrivateKey(testDelegateeSk)
	cfg := &Config{PrivateKey: nsec}
	s, err := cfg.signer()
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := newLocalSigner(testDelegatorSk)
	ctx := context.Background()

	pub, _ := s.GetPublicKey(ctx)
	if pub != testDelegateePub {
		t.Fatalf("pubkey = %s", pub)
	}

	c, err := s.NIP04Encrypt(ctx, "hello", testDelegatorPub)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := peer.NIP04Decrypt(ctx, c, testDelegateePub); err != nil || p != "hello" {
		t.Fatalf("nip04 round trip: %q, %v", p, err)
	}

	c, err = s.Encrypt(ctx, "world", testDelegatorPub)
	if err != nil {
		t.Fatal(err)
	}
	if p, err := peer.Decrypt(ctx, c, testDelegateePub); err != nil || p != "world" {
		t.Fatalf("nip44 round trip: %q, %v", p, err)
	}

	ev := nostr.Event{Kind: 1, Content: "x", CreatedAt: nostr.Now()}
	if err := cfg.sign(&ev); err != nil {
		t.Fatal(err)
	}
	if ok, _ := ev.CheckSignature(); !ok || ev.PubKey != testDelegateePub {
		t.Fatal("bad signature")
	}
}

func TestGiftWrapRoundTrip(t *testing.T) {
	sender, _ := newLocalSigner(testDelegatorSk)
	recipient, _ := newLocalSigner(testDelegateeSk)
	rumor := nostr.Event{Kind: nostr.KindDirectMessage, PubKey: testDelegatorPub, Content: "secret", CreatedAt: nostr.Now()}
	wrap, err := createGiftWrap(rumor, testDelegateePub, sender)
	if err != nil {
		t.Fatal(err)
	}
	got, err := giftUnwrap(wrap, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "secret" || got.PubKey != testDelegatorPub {
		t.Fatalf("unwrapped %+v", got)
	}
}

func TestEncryptedKeySigner(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	ncryptsec, err := nip49.Encrypt(testDelegateeSk, "pass", 4, nip49.ClientDoesNotTrackThisData)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv(passwordEnv, "wrong")
	cfg := &Config{PrivateKey: ncryptsec, profileName: "e"}
	if _, err := cfg.signer(); err == nil {
		t.Fatal("expected error with wrong password")
	}

	t.Setenv(passwordEnv, "pass")
	cfg = &Config{PrivateKey: ncryptsec, profileName: "e"}
	ev := nostr.Event{Kind: 1, Content: "x", CreatedAt: nostr.Now()}
	if err := cfg.sign(&ev); err != nil {
		t.Fatal(err)
	}
	if ev.PubKey != testDelegateePub {
		t.Fatalf("signed as %s", ev.PubKey)
	}
	if sk, err := cfg.localSecretKey(); err != nil || sk != testDelegateeSk {
		t.Fatalf("localSecretKey() = %q, %v", sk, err)
	}

	// The public key is now cached: a fresh run resolves it without the
	// password (a wrong one would fail if it were used).
	t.Setenv(passwordEnv, "wrong")
	cfg = &Config{PrivateKey: ncryptsec, profileName: "e"}
	if pub, err := cfg.pubkey(); err != nil || pub != testDelegateePub {
		t.Fatalf("pubkey() = %q, %v", pub, err)
	}
	// The cache is tied to this exact ncryptsec.
	other, _ := nip49.Encrypt(testDelegatorSk, "pass", 4, nip49.ClientDoesNotTrackThisData)
	if pub := loadCachedPubkey("e", other); pub != "" {
		t.Fatalf("cache leaked to another key: %s", pub)
	}
}

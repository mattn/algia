package main

import (
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

// Test vectors from NIP-26
const (
	testDelegatorSk  = "ee35e8bb71131c02c1d7e73231daa48e9953d329a4b701f7133c8f46dd21139c"
	testDelegatorPub = "8e0d3d3eb2881ec137a11debe736a9086715a8c8beeeda615780064d68bc25dd"
	testDelegateeSk  = "777e4f60b4aa87937e13acc84f7abcc3c93cc035cb4c1e9f7a9086dd78fffce1"
	testDelegateePub = "477318cfb5427b9cfc66a9fa376150c1ddbc62115ae27cef72417eb959691396"
	testConditions   = "kind=1&created_at>1674834236&created_at<1677426236"
	testToken        = "6f44d7fe4f1c09f3954640fb58bd12bae8bb8ff4120853c4693106c82e920e2b898f1f9ba9bd65449a987c39c0423426ab7b53910c0c6abfb41b30bc16e5f524"
)

func TestVerifyDelegationTokenSpecExample(t *testing.T) {
	if !verifyDelegationToken(testDelegatorPub, testDelegateePub, testConditions, testToken) {
		t.Fatal("spec example token should verify")
	}
	if verifyDelegationToken(testDelegatorPub, testDelegateePub, "kind=1&kind=4&created_at>1674834236&created_at<1677426236", testToken) {
		t.Fatal("token must not verify against tampered conditions")
	}
	if verifyDelegationToken(testDelegatorPub, testDelegatorPub, testConditions, testToken) {
		t.Fatal("token must not verify against a different delegatee")
	}
}

func TestCreateDelegationToken(t *testing.T) {
	token, err := createDelegationToken(testDelegatorSk, testDelegateePub, testConditions)
	if err != nil {
		t.Fatal(err)
	}
	if !verifyDelegationToken(testDelegatorPub, testDelegateePub, testConditions, token) {
		t.Fatal("created token should verify")
	}
}

func TestParseDelegationConditions(t *testing.T) {
	c, err := parseDelegationConditions(testConditions)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.kinds) != 1 || c.kinds[0] != 1 {
		t.Fatalf("kinds = %v, want [1]", c.kinds)
	}
	if c.after != 1674834236 || c.before != 1677426236 {
		t.Fatalf("range = %d..%d", c.after, c.before)
	}

	if err := c.allow(1, 1675000000); err != nil {
		t.Fatalf("kind 1 in range should be allowed: %v", err)
	}
	if err := c.allow(4, 1675000000); err == nil {
		t.Fatal("kind 4 should not be allowed")
	}
	if err := c.allow(1, 1674834236); err == nil {
		t.Fatal("created_at at lower bound should not be allowed")
	}
	if err := c.allow(1, 1677426236); err == nil {
		t.Fatal("created_at at upper bound should not be allowed")
	}

	if _, err := parseDelegationConditions("kind=1&foo=2"); err == nil {
		t.Fatal("unsupported condition should be rejected")
	}
}

func TestSignEventWithDelegation(t *testing.T) {
	nsec, err := nip19.EncodePrivateKey(testDelegateeSk)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		PrivateKey: nsec,
		Delegation: &Delegation{
			Delegator:  testDelegatorPub,
			Conditions: testConditions,
			Token:      testToken,
		},
	}

	ev := nostr.Event{
		Kind:      1,
		CreatedAt: nostr.Timestamp(1675000000),
		Content:   "Hello, world!",
		Tags:      nostr.Tags{},
	}
	if err := cfg.signEvent(&ev); err != nil {
		t.Fatal(err)
	}
	if ok, err := ev.CheckSignature(); !ok || err != nil {
		t.Fatalf("signature check failed: %v", err)
	}
	if ev.PubKey != testDelegateePub {
		t.Fatalf("event pubkey = %s, want delegatee", ev.PubKey)
	}
	tag := ev.Tags.GetFirst([]string{"delegation"})
	if tag == nil || len(*tag) != 4 {
		t.Fatal("delegation tag not attached")
	}

	pubkey, delegated := delegationDisplayPubKey(&ev)
	if !delegated || pubkey != testDelegatorPub {
		t.Fatalf("display pubkey = %s (delegated=%v), want delegator", pubkey, delegated)
	}

	// disallowed kind must be refused before signing
	bad := nostr.Event{
		Kind:      4,
		CreatedAt: nostr.Timestamp(1675000000),
		Tags:      nostr.Tags{},
	}
	if err := cfg.signEvent(&bad); err == nil {
		t.Fatal("kind 4 should be refused by delegation conditions")
	}

	// expired delegation must be refused
	expired := nostr.Event{
		Kind:      1,
		CreatedAt: nostr.Timestamp(1677426300),
		Tags:      nostr.Tags{},
	}
	if err := cfg.signEvent(&expired); err == nil {
		t.Fatal("expired delegation should be refused")
	}
}

func TestSignEventWithoutDelegation(t *testing.T) {
	nsec, err := nip19.EncodePrivateKey(testDelegateeSk)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{PrivateKey: nsec}
	ev := nostr.Event{
		Kind:      1,
		CreatedAt: nostr.Now(),
		Content:   "plain",
		Tags:      nostr.Tags{},
	}
	if err := cfg.signEvent(&ev); err != nil {
		t.Fatal(err)
	}
	if ok, err := ev.CheckSignature(); !ok || err != nil {
		t.Fatalf("signature check failed: %v", err)
	}
	if ev.Tags.GetFirst([]string{"delegation"}) != nil {
		t.Fatal("delegation tag must not be attached without delegation")
	}
	if _, delegated := delegationDisplayPubKey(&ev); delegated {
		t.Fatal("plain event must not be treated as delegated")
	}
}

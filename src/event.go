package main

import "github.com/nbd-wtf/go-nostr"

type Event struct {
	Event   *nostr.Event `json:"event"`
	Profile Profile      `json:"profile"`
}

package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/sdk"
)

// buildDeleteEvent constructs an unsigned kind 5 (deletion) event for a single target id.
// If targetKind > 0, a NIP-09 "k" tag is added.
func buildDeleteEvent(pubkey, targetID string, targetKind int, now nostr.Timestamp) (*nostr.Event, error) {
	if targetID == "" {
		return nil, errors.New("target id is empty")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: now,
		Kind:      nostr.KindDeletion,
		Tags:      nostr.Tags{nostr.Tag{"e", targetID}},
	}
	if targetKind > 0 {
		ev.Tags = append(ev.Tags, nostr.Tag{"k", strconv.Itoa(targetKind)})
	}
	return ev, nil
}

// buildLikeEvent constructs an unsigned kind 7 (reaction) event.
// content "" defaults to "+"; if emoji is set, content becomes ":name:" and an emoji tag is added.
// mentionedPubkeys are appended as "p" tags (e.g., the original author for NIP-25 hints).
// relayHint, when non-empty, is appended to the "e" tag.
func buildLikeEvent(pubkey, targetID, relayHint, content, emoji string, mentionedPubkeys []string, now nostr.Timestamp) (*nostr.Event, error) {
	if targetID == "" {
		return nil, errors.New("target id is empty")
	}
	etag := nostr.Tag{"e", targetID}
	if relayHint != "" {
		etag = append(etag, relayHint)
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: now,
		Kind:      nostr.KindReaction,
		Tags:      nostr.Tags{etag},
		Content:   content,
	}
	if emoji != "" {
		if ev.Content == "" {
			ev.Content = "like"
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", ev.Content, emoji})
		ev.Content = ":" + ev.Content + ":"
	}
	if ev.Content == "" {
		ev.Content = "+"
	}
	for _, p := range mentionedPubkeys {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", p})
	}
	return ev, nil
}

// buildRepostEvent constructs an unsigned kind 6 (repost) event.
// mentionedPubkeys are appended as "p" tags (typically the original author).
func buildRepostEvent(pubkey, targetID, relayHint string, mentionedPubkeys []string, now nostr.Timestamp) (*nostr.Event, error) {
	if targetID == "" {
		return nil, errors.New("target id is empty")
	}
	etag := nostr.Tag{"e", targetID}
	if relayHint != "" {
		etag = append(etag, relayHint)
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: now,
		Kind:      nostr.KindRepost,
		Tags:      nostr.Tags{etag},
		Content:   "",
	}
	for _, p := range mentionedPubkeys {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", p})
	}
	return ev, nil
}

// buildPostEvent constructs an unsigned text-note (or NIP-23 article) event.
// mentionPubkeys must already be resolved to hex pubkeys (the caller is responsible for input→profile resolution).
func buildPostEvent(arg *postArg, pubkey string, mentionPubkeys []string, now nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(arg.content) == "" {
		return nil, errors.New("content is empty")
	}
	if arg.articleName != "" && arg.articleTitle == "" {
		return nil, errors.New("article-title is required when article-name is set")
	}

	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: now,
		Content:   arg.content,
		Tags:      nostr.Tags{},
	}
	clientTag(ev)

	for _, entry := range extractLinks(ev.Content) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"r", entry.text})
	}

	for _, u := range arg.emoji {
		tok := strings.SplitN(u, "=", 2)
		if len(tok) != 2 {
			return nil, usageError
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", tok[0], tok[1]})
	}
	for _, entry := range extractEmojis(ev.Content) {
		name := strings.Trim(entry.text, ":")
		if icon, ok := arg.cfg.Emojis[name]; ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", name, icon})
		}
	}

	var mentions []string
	for _, u := range mentionPubkeys {
		npub, err := nip19.EncodePublicKey(u)
		if err != nil {
			return nil, err
		}
		mentions = append(mentions, "nostr:"+npub)
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", u})
	}
	if len(mentions) > 0 {
		ev.Content = strings.Join(mentions, " ") + " " + ev.Content
	}

	if arg.sensitive != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"content-warning", arg.sensitive})
	}
	if arg.geohash != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"g", arg.geohash})
	}

	hashtag := nostr.Tag{"t"}
	for _, m := range extractTags(ev.Content) {
		hashtag = append(hashtag, m.text)
	}
	if len(hashtag) > 1 {
		ev.Tags = ev.Tags.AppendUnique(hashtag)
	}

	for _, t := range arg.tags {
		name, value, found := strings.Cut(t, "=")
		tag := nostr.Tag{name}
		if found {
			tag = append(tag, strings.Split(value, ";")...)
		}
		if len(tag) == 0 {
			continue
		}
		if tag[0] == "client" {
			// Overwrite an existing client tag rather than appending duplicates.
			tags := make(nostr.Tags, 0, len(ev.Tags))
			for _, existing := range ev.Tags {
				if len(existing) > 0 && existing[0] == "client" {
					continue
				}
				tags = append(tags, existing)
			}
			ev.Tags = append(tags, tag)
		} else {
			ev.Tags = ev.Tags.AppendUnique(tag)
		}
	}

	if arg.articleName != "" {
		ev.Kind = nostr.KindArticle
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"d", arg.articleName})
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"title", arg.articleTitle})
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"summary", arg.articleSummary})
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"published_at", fmt.Sprint(now)})
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"a", fmt.Sprintf("%d:%s:%s", ev.Kind, ev.PubKey, arg.articleName), "wss://yabu.me"})
	} else {
		ev.Kind = nostr.KindTextNote
	}
	return ev, nil
}

// replyOpts captures everything buildReplyEvent needs.
type replyOpts struct {
	Content   string
	ReplyToID string
	RelayHint string
	Quote     bool
	Sensitive string
	Geohash   string
	Emojis    []string // "name=url" pairs from --emoji flags
}

// buildReplyEvent constructs an unsigned kind 1 (text-note) reply event.
// cfgEmojis is the configured shortcode→icon map for inline :name: emoji expansion.
func buildReplyEvent(pubkey string, opts replyOpts, cfgEmojis map[string]string, now nostr.Timestamp) (*nostr.Event, error) {
	if strings.TrimSpace(opts.Content) == "" {
		return nil, errors.New("content is empty")
	}
	if opts.ReplyToID == "" {
		return nil, errors.New("reply target id is empty")
	}
	ev := &nostr.Event{
		PubKey:    pubkey,
		CreatedAt: now,
		Kind:      nostr.KindTextNote,
		Content:   opts.Content,
		Tags:      nostr.Tags{},
	}
	clientTag(ev)

	for _, entry := range extractLinks(ev.Content) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"r", entry.text})
	}
	for _, u := range opts.Emojis {
		tok := strings.SplitN(u, "=", 2)
		if len(tok) != 2 {
			return nil, usageError
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", tok[0], tok[1]})
	}
	for _, entry := range extractEmojis(ev.Content) {
		name := strings.Trim(entry.text, ":")
		if icon, ok := cfgEmojis[name]; ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", name, icon})
		}
	}
	if opts.Sensitive != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"content-warning", opts.Sensitive})
	}
	if opts.Geohash != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"g", opts.Geohash})
	}
	hashtag := nostr.Tag{"t"}
	for _, m := range extractTags(ev.Content) {
		hashtag = append(hashtag, m.text)
	}
	if len(hashtag) > 1 {
		ev.Tags = ev.Tags.AppendUnique(hashtag)
	}

	marker := "reply"
	if opts.Quote {
		marker = "mention"
	}
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", opts.ReplyToID, opts.RelayHint, marker})
	return ev, nil
}

// resolveMentions resolves a list of npub/nprofile/etc strings into hex pubkeys.
// Returns the same length as input, in the same order.
func resolveMentions(ctx context.Context, us []string) ([]string, error) {
	if len(us) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(us))
	for _, u := range us {
		if pp := sdk.InputToProfile(ctx, u); pp != nil {
			out = append(out, pp.PublicKey)
		} else {
			return nil, fmt.Errorf("failed to parse pubkey from '%s'", u)
		}
	}
	return out, nil
}

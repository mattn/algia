package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/fatih/color"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func doDMList(cCtx *cli.Context) error {
	j := cCtx.Bool("json")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.FindRelay(Relay{Read: true})
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	// get followers
	followsMap, err := cfg.GetFollows(relay, cCtx.String("a"))
	if err != nil {
		return err
	}

	var sk string
	var npub string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if npub, err = nostr.GetPublicKey(sk); err != nil {
		return err
	}

	// get timeline
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindEncryptedDirectMessage},
		Authors: []string{npub},
		Limit:   9999,
	}

	evs := cfg.Events(filter)
	type entry struct {
		name   string
		pubkey string
	}
	users := []entry{}
	m := map[string]struct{}{}
	for _, ev := range evs {
		p := ev.Tags.GetFirst([]string{"p"}).Value()
		if _, ok := m[p]; ok {
			continue
		}
		if profile, ok := followsMap[p]; ok {
			m[p] = struct{}{}
			p, _ = nip19.EncodePublicKey(p)
			users = append(users, entry{
				name:   profile.DisplayName,
				pubkey: p,
			})
		}
	}
	if j {
		for _, user := range users {
			json.NewEncoder(os.Stdout).Encode(user)
		}
		return nil
	}

	for _, user := range users {
		color.Set(color.FgHiRed)
		fmt.Print(user.name)
		color.Set(color.Reset)
		fmt.Print(": ")
		color.Set(color.FgHiBlue)
		fmt.Println(user.pubkey)
		color.Set(color.Reset)
	}
	return nil
}

func doDMTimeline(cCtx *cli.Context) error {
	u := cCtx.String("u")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.FindRelay(Relay{Read: true})
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	var sk string
	var npub string
	var err error
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if npub, err = nostr.GetPublicKey(sk); err != nil {
		return err
	}

	var pub string
	if _, s, err := nip19.Decode(u); err != nil {
		return err
	} else {
		pub = s.(string)
	}
	// get followers
	followsMap, err := cfg.GetFollows(relay, cCtx.String("a"))
	if err != nil {
		return err
	}

	// get timeline
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindEncryptedDirectMessage},
		Authors: []string{npub, pub},
		Tags:    nostr.TagMap{"p": []string{npub, pub}},
		Limit:   9999,
	}

	evs := cfg.Events(filter)
	cfg.PrintEvents(evs, followsMap, j, extra)
	return nil
}

func doDMPost(cCtx *cli.Context) error {
	u := cCtx.String("u")
	stdin := cCtx.Bool("stdin")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	sensitive := cCtx.String("sensitive")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var pub string
	if _, s, err := nip19.Decode(u); err != nil {
		return err
	} else {
		pub = s.(string)
	}

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	ev := nostr.Event{}
	if npub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(npub); err != nil {
			return err
		}
		ev.PubKey = npub
	} else {
		return err
	}

	if stdin {
		b, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		ev.Content = string(b)
	} else {
		ev.Content = strings.Join(cCtx.Args().Slice(), "\n")
	}
	if strings.TrimSpace(ev.Content) == "" {
		return errors.New("content is empty")
	}

	if sensitive != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"content-warning", sensitive})
	}

	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", pub})
	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindEncryptedDirectMessage

	ss, err := nip04.ComputeSharedSecret(ev.PubKey, sk)
	if err != nil {
		return err
	}
	ev.Content, err = nip04.Encrypt(ev.Content, ss)
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		status, err := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status, err)
		}
		if err == nil && status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot post")
	}
	return nil
}

func doPost(cCtx *cli.Context) error {
	stdin := cCtx.Bool("stdin")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	sensitive := cCtx.String("sensitive")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	ev := nostr.Event{}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if stdin {
		b, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		ev.Content = string(b)
	} else {
		ev.Content = strings.Join(cCtx.Args().Slice(), "\n")
	}
	if strings.TrimSpace(ev.Content) == "" {
		return errors.New("content is empty")
	}

	for i, u := range cCtx.StringSlice("u") {
		ev.Content = fmt.Sprintf("#[%d] ", i) + ev.Content
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", u, "", "reply"})
	}

	if sensitive != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"content-warning", sensitive})
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindTextNote
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		status, err := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status, err)
		}
		if err == nil && status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot post")
	}
	return nil
}

func doReply(cCtx *cli.Context) error {
	stdin := cCtx.Bool("stdin")
	id := cCtx.String("id")
	quote := cCtx.Bool("quote")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	ev := nostr.Event{}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if _, tmp, err := nip19.Decode(id); err == nil {
		if s, ok := tmp.(string); ok {
			id = s
		} else if s, ok := tmp.(nostr.EventPointer); ok {
			id = s.ID
		}
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindTextNote
	if stdin {
		b, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		ev.Content = string(b)
	} else {
		ev.Content = strings.Join(cCtx.Args().Slice(), "\n")
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		if !quote {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id, relay.URL, "reply"})
		} else {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id, relay.URL, "mention"})
		}
		if err := ev.Sign(sk); err != nil {
			return
		}
		status, err := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status, err)
		}
		if err == nil && status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot reply")
	}
	return nil
}

func doRepost(cCtx *cli.Context) error {
	id := cCtx.String("id")

	cfg := cCtx.App.Metadata["config"].(*Config)

	ev := nostr.Event{}
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if _, tmp, err := nip19.Decode(id); err == nil {
		if s, ok := tmp.(string); ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", s})
		} else if s, ok := tmp.(nostr.EventPointer); ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", s.ID})
		}
	} else {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	}
	filter := nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{id},
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindBoost
	ev.Content = ""

	var first atomic.Bool
	first.Store(true)

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		if first.Load() {
			for _, tmp := range relay.QuerySync(context.Background(), filter) {
				ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", tmp.ID})
			}
			first.Store(false)
			if err := ev.Sign(sk); err != nil {
				return
			}
		}
		status, err := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status, err)
		}
		if err == nil && status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot repost")
	}
	return nil
}

func doLike(cCtx *cli.Context) error {
	id := cCtx.String("id")

	cfg := cCtx.App.Metadata["config"].(*Config)

	ev := nostr.Event{}
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if _, tmp, err := nip19.Decode(id); err == nil {
		if s, ok := tmp.(string); ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", s})
		} else if s, ok := tmp.(nostr.EventPointer); ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", s.ID})
		}
	} else {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	}
	filter := nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{id},
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindReaction
	ev.Content = "+"

	var first atomic.Bool
	first.Store(true)

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		if first.Load() {
			for _, tmp := range relay.QuerySync(context.Background(), filter) {
				ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", tmp.ID})
			}
			first.Store(false)
			if err := ev.Sign(sk); err != nil {
				return
			}
		}
		status, err := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status, err)
		}
		if err == nil && status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot like")
	}
	return nil
}

func doDelete(cCtx *cli.Context) error {
	id := cCtx.String("id")

	cfg := cCtx.App.Metadata["config"].(*Config)

	ev := nostr.Event{}
	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if _, tmp, err := nip19.Decode(id); err == nil {
		if s, ok := tmp.(string); ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", s})
		} else if s, ok := tmp.(nostr.EventPointer); ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", s.ID})
		}
	} else {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	}

	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindDeletion
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		status, err := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status, err)
		}
		if err == nil && status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot delete")
	}
	return nil
}

func doSearch(cCtx *cli.Context) error {
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.FindRelay(Relay{Search: true})
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	// get followers
	followsMap, err := cfg.GetFollows(relay, cCtx.String("a"))
	if err != nil {
		return err
	}
	var follows []string
	for k := range followsMap {
		follows = append(follows, k)
	}

	// get timeline
	filter := nostr.Filter{
		Kinds:  []int{nostr.KindTextNote},
		Search: strings.Join(cCtx.Args().Slice(), " "),
		Limit:  n,
	}

	evs := cfg.Events(filter)
	cfg.PrintEvents(evs, followsMap, j, extra)
	return nil
}

func doTimeline(cCtx *cli.Context) error {
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)
	relay := cfg.FindRelay(Relay{Read: true})
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	// get followers
	followsMap, err := cfg.GetFollows(relay, cCtx.String("a"))
	if err != nil {
		return err
	}
	var follows []string
	for k := range followsMap {
		follows = append(follows, k)
	}

	// get timeline
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindTextNote},
		Authors: follows,
		Limit:   n,
	}

	evs := cfg.Events(filter)
	cfg.PrintEvents(evs, followsMap, j, extra)
	return nil
}

func doPowa(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	ev := nostr.Event{}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	ev.Content = "ぽわ〜"
	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindTextNote
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		status, err := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status, err)
		}
		if err == nil && status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot post")
	}
	return nil
}

func doPuru(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	ev := nostr.Event{}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	ev.Content = "(((( ˙꒳​˙  ))))ﾌﾟﾙﾌﾟﾙﾌﾟﾙﾌﾟﾙﾌﾟﾙﾌﾟﾙﾌﾟﾙ"
	ev.CreatedAt = time.Now()
	ev.Kind = nostr.KindTextNote
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		status, err := relay.Publish(context.Background(), ev)
		if cfg.verbose {
			fmt.Fprintln(os.Stderr, relay.URL, status, err)
		}
		if err == nil && status != nostr.PublishStatusFailed {
			success.Add(1)
		}
	})
	if success.Load() == 0 {
		return errors.New("cannot post")
	}
	return nil
}

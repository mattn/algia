package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/urfave/cli/v2"

	"github.com/fatih/color"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/sdk"
)

func doDMList(cCtx *cli.Context) error {
	j := cCtx.Bool("json")

	cfg := cCtx.App.Metadata["config"].(*Config)

	// get followers
	followsMap, err := cfg.GetFollows(cCtx.String("a"))
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
		} else {
			users = append(users, entry{
				name:   p,
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

	if u == "me" {
		u = npub
	}
	var pub string
	if pp := sdk.InputToProfile(context.TODO(), u); pp == nil {
		return fmt.Errorf("failed to parse pubkey from '%s'", u)
	} else {
		pub = pp.PublicKey
	}
	// get followers
	followsMap, err := cfg.GetFollows(cCtx.String("a"))
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

	if u == "me" {
		u = ev.PubKey
	}
	var pub string
	if pp := sdk.InputToProfile(context.TODO(), u); pp == nil {
		return fmt.Errorf("failed to parse pubkey from '%s'", u)
	} else {
		pub = pp.PublicKey
	}

	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", pub})
	ev.CreatedAt = nostr.Now()
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

	ev.Tags = nostr.Tags{}

	for _, entry := range extractLinks(ev.Content) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"r", entry.text})
	}

	for _, u := range cCtx.StringSlice("emoji") {
		tok := strings.SplitN(u, "=", 2)
		if len(tok) != 2 {
			return cli.ShowSubcommandHelp(cCtx)
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", tok[0], tok[1]})
	}
	for _, entry := range extractEmojis(ev.Content) {
		name := strings.Trim(entry.text, ":")
		if icon, ok := cfg.Emojis[name]; ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", name, icon})
		}
	}

	for i, u := range cCtx.StringSlice("u") {
		ev.Content = fmt.Sprintf("#[%d] ", i) + ev.Content
		if pp := sdk.InputToProfile(context.TODO(), u); pp == nil {
			return fmt.Errorf("failed to parse pubkey from '%s'", u)
		} else {
			u = pp.PublicKey
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", u})
	}

	if sensitive != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"content-warning", sensitive})
	}

	hashtag := nostr.Tag{"h"}
	for _, m := range regexp.MustCompile(`#[a-zA-Z0-9]+`).FindAllStringSubmatchIndex(ev.Content, -1) {
		hashtag = append(hashtag, ev.Content[m[0]+1:m[1]])
	}
	if len(hashtag) > 1 {
		ev.Tags = ev.Tags.AppendUnique(hashtag)
	}

	ev.CreatedAt = nostr.Now()
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

	if evp := sdk.InputToEventPointer(id); evp == nil {
		return fmt.Errorf("failed to parse event from '%s'", id)
	} else {
		id = evp.ID
	}

	ev.CreatedAt = nostr.Now()
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
	if strings.TrimSpace(ev.Content) == "" {
		return errors.New("content is empty")
	}

	ev.Tags = nostr.Tags{}

	for _, entry := range extractLinks(ev.Content) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"r", entry.text})
	}

	for _, u := range cCtx.StringSlice("emoji") {
		tok := strings.SplitN(u, "=", 2)
		if len(tok) != 2 {
			return cli.ShowSubcommandHelp(cCtx)
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", tok[0], tok[1]})
	}
	for _, entry := range extractEmojis(ev.Content) {
		name := strings.Trim(entry.text, ":")
		if icon, ok := cfg.Emojis[name]; ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", name, icon})
		}
	}

	if sensitive != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"content-warning", sensitive})
	}

	hashtag := nostr.Tag{"h"}
	for _, m := range regexp.MustCompile(`#[a-zA-Z0-9]+`).FindAllStringSubmatchIndex(ev.Content, -1) {
		hashtag = append(hashtag, ev.Content[m[0]+1:m[1]])
	}
	if len(hashtag) > 1 {
		ev.Tags = ev.Tags.AppendUnique(hashtag)
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

	if evp := sdk.InputToEventPointer(id); evp == nil {
		return fmt.Errorf("failed to parse event from '%s'", id)
	} else {
		id = evp.ID
	}
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	filter := nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{id},
	}

	ev.CreatedAt = nostr.Now()
	ev.Kind = nostr.KindRepost
	ev.Content = ""

	var first atomic.Bool
	first.Store(true)

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		if first.Load() {
			evs, err := relay.QuerySync(context.Background(), filter)
			if err != nil {
				return
			}
			for _, tmp := range evs {
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

func doUnrepost(cCtx *cli.Context) error {
	id := cCtx.String("id")
	if evp := sdk.InputToEventPointer(id); evp == nil {
		return fmt.Errorf("failed to parse event from '%s'", id)
	} else {
		id = evp.ID
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return err
	}
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindRepost},
		Authors: []string{pub},
		Tags:    nostr.TagMap{"e": []string{id}},
	}
	var repostID string
	var mu sync.Mutex
	cfg.Do(Relay{Read: true}, func(relay *nostr.Relay) {
		evs, err := relay.QuerySync(context.Background(), filter)
		if err != nil {
			return
		}
		mu.Lock()
		if len(evs) > 0 && repostID == "" {
			repostID = evs[0].ID
		}
		mu.Unlock()
	})

	var ev nostr.Event
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", repostID})
	ev.CreatedAt = nostr.Now()
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
		return errors.New("cannot unrepost")
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

	if evp := sdk.InputToEventPointer(id); evp == nil {
		return fmt.Errorf("failed to parse event from '%s'", id)
	} else {
		id = evp.ID
	}
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	filter := nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{id},
	}

	ev.CreatedAt = nostr.Now()
	ev.Kind = nostr.KindReaction
	ev.Content = cCtx.String("content")
	emoji := cCtx.String("emoji")
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

	var first atomic.Bool
	first.Store(true)

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
		if first.Load() {
			evs, err := relay.QuerySync(context.Background(), filter)
			if err != nil {
				return
			}
			for _, tmp := range evs {
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

func doUnlike(cCtx *cli.Context) error {
	id := cCtx.String("id")
	if evp := sdk.InputToEventPointer(id); evp == nil {
		return fmt.Errorf("failed to parse event from '%s'", id)
	} else {
		id = evp.ID
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return err
	}
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindReaction},
		Authors: []string{pub},
		Tags:    nostr.TagMap{"e": []string{id}},
	}
	var likeID string
	var mu sync.Mutex
	cfg.Do(Relay{Read: true}, func(relay *nostr.Relay) {
		evs, err := relay.QuerySync(context.Background(), filter)
		if err != nil {
			return
		}
		mu.Lock()
		if len(evs) > 0 && likeID == "" {
			likeID = evs[0].ID
		}
		mu.Unlock()
	})

	var ev nostr.Event
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", likeID})
	ev.CreatedAt = nostr.Now()
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
		return errors.New("cannot unlike")
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

	if evp := sdk.InputToEventPointer(id); evp == nil {
		return fmt.Errorf("failed to parse event from '%s'", id)
	} else {
		id = evp.ID
	}
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	ev.CreatedAt = nostr.Now()
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

	// get followers
	var followsMap map[string]Profile
	var err error
	if j && !extra {
		followsMap = make(map[string]Profile)
	} else {
		followsMap, err = cfg.GetFollows(cCtx.String("a"))
		if err != nil {
			return err
		}
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

func doStream(cCtx *cli.Context) error {
	kinds := cCtx.IntSlice("kind")
	authors := cCtx.StringSlice("author")
	f := cCtx.Bool("follow")
	pattern := cCtx.String("pattern")
	reply := cCtx.String("reply")

	var re *regexp.Regexp
	if pattern != "" {
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return err
		}
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	relay := cfg.FindRelay(Relay{Read: true})
	if relay == nil {
		return errors.New("cannot connect relays")
	}
	defer relay.Close()

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err != nil {
		return err
	} else {
		sk = s.(string)
	}
	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return err
	}

	// get followers
	var follows []string
	if f {
		followsMap, err := cfg.GetFollows(cCtx.String("a"))
		if err != nil {
			return err
		}
		for k := range followsMap {
			follows = append(follows, k)
		}
	} else {
		follows = authors
	}

	since := nostr.Now()
	filter := nostr.Filter{
		Kinds:   kinds,
		Authors: follows,
		Since:   &since,
	}

	sub, err := relay.Subscribe(context.Background(), nostr.Filters{filter})
	if err != nil {
		return err
	}
	for ev := range sub.Events {
		if ev.Kind == nostr.KindTextNote {
			if re != nil && !re.MatchString(ev.Content) {
				continue
			}
			json.NewEncoder(os.Stdout).Encode(ev)
			if reply != "" {
				var evr nostr.Event
				evr.PubKey = pub
				evr.Content = reply
				evr.Tags = evr.Tags.AppendUnique(nostr.Tag{"e", ev.ID, "", "reply"})
				evr.CreatedAt = nostr.Now()
				evr.Kind = nostr.KindTextNote
				if err := evr.Sign(sk); err != nil {
					return err
				}
				cfg.Do(Relay{Write: true}, func(relay *nostr.Relay) {
					relay.Publish(context.Background(), evr)
				})
			}
		} else {
			json.NewEncoder(os.Stdout).Encode(ev)
		}
	}
	return nil
}

func doTimeline(cCtx *cli.Context) error {
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)

	// get followers
	followsMap, err := cfg.GetFollows(cCtx.String("a"))
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

func postMsg(cCtx *cli.Context, msg string) error {
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

	ev.Content = msg
	ev.CreatedAt = nostr.Now()
	ev.Kind = nostr.KindTextNote
	ev.Tags = nostr.Tags{}
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

func doPowa(cCtx *cli.Context) error {
	return postMsg(cCtx, "ぽわ〜")
}

func doPuru(cCtx *cli.Context) error {
	return postMsg(cCtx, "(((( ˙꒳​˙  ))))ﾌﾟﾙﾌﾟﾙﾌﾟﾙﾌﾟﾙﾌﾟﾙﾌﾟﾙﾌﾟﾙ")
}

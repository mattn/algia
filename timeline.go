package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/sdk"
)

var usageError = errors.New("usage")

func doPost(cCtx *cli.Context) error {
	stdin := cCtx.Bool("stdin")
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	articleName := cCtx.String("article-name")
	articleTitle := cCtx.String("article-title")
	articleSummary := cCtx.String("article-summary")
	if articleName != "" && articleTitle == "" {
		return cli.ShowSubcommandHelp(cCtx)
	}

	var content string
	if stdin {
		b, err := ioutil.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		content = string(b)
	} else {
		content = strings.Join(cCtx.Args().Slice(), "\n")
	}
	return callPost(&postArg{
		cfg:            cCtx.App.Metadata["config"].(*Config),
		content:        content,
		sensitive:      cCtx.String("sensitive"),
		geohash:        cCtx.String("geohash"),
		articleName:    articleName,
		articleTitle:   articleTitle,
		articleSummary: articleSummary,
		emoji:          cCtx.StringSlice("emoji"),
		us:             cCtx.StringSlice("u"),
	})
}

type postArg struct {
	cfg            *Config
	content        string
	sensitive      string
	geohash        string
	articleName    string
	articleTitle   string
	articleSummary string
	emoji          []string
	us             []string
}

func callPost(arg *postArg) error {
	var sk string
	if _, s, err := nip19.Decode(arg.cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
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

	ev.Content = arg.content
	if strings.TrimSpace(ev.Content) == "" {
		return errors.New("content is empty")
	}

	ev.Tags = nostr.Tags{}
	clientTag(&ev)

	for _, entry := range extractLinks(ev.Content) {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"r", entry.text})
	}

	for _, u := range arg.emoji {
		tok := strings.SplitN(u, "=", 2)
		if len(tok) != 2 {
			return usageError
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", tok[0], tok[1]})
	}
	for _, entry := range extractEmojis(ev.Content) {
		name := strings.Trim(entry.text, ":")
		if icon, ok := arg.cfg.Emojis[name]; ok {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", name, icon})
		}
	}

	for i, u := range arg.us {
		ev.Content = fmt.Sprintf("#[%d] ", i) + ev.Content
		if pp := sdk.InputToProfile(context.TODO(), u); pp != nil {
			u = pp.PublicKey
		} else {
			return fmt.Errorf("failed to parse pubkey from '%s'", u)
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", u})
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

	ev.CreatedAt = nostr.Now()
	if arg.articleName != "" {
		ev.Kind = nostr.KindArticle
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"d", arg.articleName})
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"title", arg.articleTitle})
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"summary", arg.articleSummary})
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"published_at", fmt.Sprint(nostr.Now())})
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"a", fmt.Sprintf("%d:%s:%s", ev.Kind, ev.PubKey, arg.articleName), "wss://yabu.me"})
	} else {
		ev.Kind = nostr.KindTextNote
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot post")
	}
	if arg.cfg.verbose {
		if id, err := nip19.EncodeNote(ev.ID); err == nil {
			fmt.Println(id)
		}
	}
	return nil
}

func doEvent(cCtx *cli.Context) error {
	stdin := cCtx.Bool("stdin")
	kind := cCtx.Int("kind")
	content := cCtx.String("content")
	tags := cCtx.StringSlice("tag")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
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
		ev.Content = content
	}

	ev.Tags = nostr.Tags{}
	clientTag(&ev)

	for _, tag := range tags {
		name, value, found := strings.Cut(tag, "=")
		tag := []string{name}
		if found {
			// tags may also contain extra elements separated with a ";"
			tag = append(tag, strings.Split(value, ";")...)
		}
		ev.Tags = ev.Tags.AppendUnique(tag)
	}

	ev.Kind = kind
	ev.CreatedAt = nostr.Now()

	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot post")
	}
	if cfg.verbose {
		if id, err := nip19.EncodeNote(ev.ID); err == nil {
			fmt.Println(id)
		}
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
	geohash := cCtx.String("geohash")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
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

	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
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
	clientTag(&ev)

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

	if geohash != "" {
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"g", geohash})
	}

	hashtag := nostr.Tag{"t"}
	for _, m := range extractTags(ev.Content) {
		hashtag = append(hashtag, m.text)
	}
	if len(hashtag) > 1 {
		ev.Tags = ev.Tags.AppendUnique(hashtag)
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if !quote {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id, relay.URL, "reply"})
		} else {
			ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id, relay.URL, "mention"})
		}
		if err := ev.Sign(sk); err != nil {
			return true
		}
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
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
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
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
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if first.Load() {
			evs, err := relay.QuerySync(ctx, filter)
			if err != nil {
				return true
			}
			for _, tmp := range evs {
				ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", tmp.ID})
			}
			first.Store(false)
			if err := ev.Sign(sk); err != nil {
				return true
			}
		}
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot repost")
	}
	return nil
}

func doUnrepost(cCtx *cli.Context) error {
	id := cCtx.String("id")
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
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
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		evs, err := relay.QuerySync(ctx, filter)
		if err != nil {
			return true
		}
		mu.Lock()
		if len(evs) > 0 && repostID == "" {
			repostID = evs[0].ID
		}
		mu.Unlock()
		return true
	})

	var ev nostr.Event
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", repostID})
	ev.CreatedAt = nostr.Now()
	ev.Kind = nostr.KindDeletion
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot unrepost")
	}
	return nil
}

func doLike(cCtx *cli.Context) error {
	return callLike(&likeArg{
		cfg:     cCtx.App.Metadata["config"].(*Config),
		id:      cCtx.String("id"),
		content: cCtx.String("content"),
		emoji:   cCtx.String("emoji"),
	})
}

type likeArg struct {
	cfg     *Config
	id      string
	content string
	emoji   string
}

func callLike(arg *likeArg) error {
	ev := nostr.Event{}
	var sk string
	if _, s, err := nip19.Decode(arg.cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if evp := sdk.InputToEventPointer(arg.id); evp != nil {
		arg.id = evp.ID
	} else {
		return fmt.Errorf("failed to parse event from '%s'", arg.id)
	}
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", arg.id})
	filter := nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{arg.id},
	}

	ev.CreatedAt = nostr.Now()
	ev.Kind = nostr.KindReaction
	ev.Content = arg.content
	if arg.emoji != "" {
		if ev.Content == "" {
			ev.Content = "like"
		}
		ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"emoji", ev.Content, arg.emoji})
		ev.Content = ":" + ev.Content + ":"
	}
	if ev.Content == "" {
		ev.Content = "+"
	}

	var first atomic.Bool
	first.Store(true)

	var success atomic.Int64
	arg.cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if first.Load() {
			evs, err := relay.QuerySync(ctx, filter)
			if err != nil {
				return true
			}
			for _, tmp := range evs {
				ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"p", tmp.ID})
			}
			first.Store(false)
			if err := ev.Sign(sk); err != nil {
				return true
			}
			return true
		}
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot like")
	}
	return nil
}

func doUnlike(cCtx *cli.Context) error {
	id := cCtx.String("id")
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
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
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		evs, err := relay.QuerySync(ctx, filter)
		if err != nil {
			return true
		}
		mu.Lock()
		if len(evs) > 0 && likeID == "" {
			likeID = evs[0].ID
		}
		mu.Unlock()
		return true
	})

	var ev nostr.Event
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", likeID})
	ev.CreatedAt = nostr.Now()
	ev.Kind = nostr.KindDeletion
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
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
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	if pub, err := nostr.GetPublicKey(sk); err == nil {
		if _, err := nip19.EncodePublicKey(pub); err != nil {
			return err
		}
		ev.PubKey = pub
	} else {
		return err
	}

	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
	}
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"e", id})
	ev.CreatedAt = nostr.Now()
	ev.Kind = nostr.KindDeletion
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
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

	// get timeline
	filters := nostr.Filters{
		{
			Kinds:  []int{nostr.KindTextNote},
			Search: strings.Join(cCtx.Args().Slice(), " "),
			Limit:  n,
		},
	}

	evs, err := cfg.QueryEvents(filters)
	if err != nil {
		return err
	}
	cfg.PrintEvents(evs, nil, j, extra)
	return nil
}

func doBroadcast(cCtx *cli.Context) error {
	id := cCtx.String("id")
	from := cCtx.String("relay")

	var filter nostr.Filter

	if evp := sdk.InputToEventPointer(id); evp == nil {
		epp := sdk.InputToProfile(context.Background(), id)
		if epp == nil {
			return fmt.Errorf("failed to parse note/npub from '%s'", id)
		}
		filter = nostr.Filter{
			Kinds:   []int{nostr.KindProfileMetadata},
			Authors: []string{epp.PublicKey},
		}
	} else {
		filter = nostr.Filter{
			IDs: []string{evp.ID},
		}
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var ev *nostr.Event
	var mu sync.Mutex

	if from != "" {
		ctx := context.Background()
		relay, err := nostr.RelayConnect(ctx, from)
		if err != nil {
			return err
		}
		defer relay.Close()
		evs, err := relay.QuerySync(ctx, filter)
		if err != nil {
			return err
		}
		if len(evs) > 0 {
			ev = evs[0]
		}
	} else {
		cfg.Do(Relay{Read: true}, func(ctx context.Context, relay *nostr.Relay) bool {
			if relay.URL == from {
				return true
			}
			evs, err := relay.QuerySync(ctx, filter)
			if err != nil {
				return true
			}
			if len(evs) > 0 {
				mu.Lock()
				ev = evs[0]
				mu.Unlock()
			}
			return false
		})
	}

	if ev == nil {
		return fmt.Errorf("failed to get event '%s'", id)
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})
	if success.Load() == 0 {
		return errors.New("cannot broadcast")
	}
	return nil
}

func doStream(cCtx *cli.Context) error {
	kinds := cCtx.IntSlice("kind")
	authors := cCtx.StringSlice("author")
	f := cCtx.Bool("follow")
	pattern := cCtx.String("pattern")
	reply := cCtx.String("reply")
	tags := cCtx.StringSlice("tag")
	j := cCtx.Bool("json")
	global := cCtx.Bool("global")

	var re *regexp.Regexp
	if pattern != "" {
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return err
		}
	}

	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
	}
	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return err
	}

	// get followers
	var follows []string
	if global {
		follows = nil
	} else {
		if f {
			follows = append(follows, cfg.FollowList...)
		} else {
			for _, author := range authors {
				if pp := sdk.InputToProfile(context.TODO(), author); pp != nil {
					follows = append(follows, pp.PublicKey)
				} else {
					return fmt.Errorf("failed to parse pubkey from '%s'", author)
				}
			}
		}
	}

	since := nostr.Now()
	filter := nostr.Filter{
		Kinds:   kinds,
		Authors: follows,
		Since:   &since,
		Tags:    nostr.TagMap{},
	}

	for _, tag := range tags {
		name, value, found := strings.Cut(tag, "=")
		tag := []string{}
		if found {
			// tags may also contain extra elements separated with a ";"
			tag = append(tag, strings.Split(value, ";")...)
		}
		filter.Tags[name] = tag
	}

	relays := []string{}
	for rurl, relay := range cfg.Relays {
		if !relay.Global {
			continue
		}
		relays = append(relays, rurl)
	}
	if len(relays) == 0 {
		for rurl, relay := range cfg.Relays {
			if !relay.Read {
				continue
			}
			relays = append(relays, rurl)
		}
	}
	sub := cfg.pool.SubMany(context.Background(), relays, nostr.Filters{filter})

	if reply == "" {
		if j {
			for ev := range sub {
				json.NewEncoder(os.Stdout).Encode(ev)
			}
		} else {
			for ev := range sub {
				cfg.PrintEvent(ev.Event, j, false)
			}
		}
	} else {
		for ev := range sub {
			if re != nil && !re.MatchString(ev.Content) {
				continue
			}
			var evr nostr.Event
			evr.PubKey = pub
			evr.Content = reply
			evr.Tags = nostr.Tags{}
			clientTag(&evr)
			for _, tag := range ev.Tags {
				if len(tag) > 0 && tag[0] != "e" && tag[0] != "p" {
					evr.Tags = evr.Tags.AppendUnique(tag)
				}
			}
			evr.Tags = evr.Tags.AppendUnique(nostr.Tag{"e", ev.ID, "", "reply"})
			evr.CreatedAt = nostr.Now()
			evr.Kind = ev.Kind
			if err := evr.Sign(sk); err != nil {
				return err
			}
			cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
				relay.Publish(ctx, evr)
				return true
			})
		}
	}

	return nil
}

func doTimeline(cCtx *cli.Context) error {
	u := cCtx.String("u")
	n := cCtx.Int("n")
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")
	article := cCtx.Bool("article")
	global := cCtx.Bool("global")

	cfg := cCtx.App.Metadata["config"].(*Config)

	var follows []string
	if global {
		follows = nil
	} else {
		if u == "" {
			follows = append(follows, cfg.FollowList...)
			if len(follows) == 0 {
				return fmt.Errorf("no follows found. Please follow someone first.")
			}
		} else {
			if pp := sdk.InputToProfile(context.TODO(), u); pp != nil {
				u = pp.PublicKey
			} else {
				return fmt.Errorf("failed to parse pubkey from '%s'", u)
			}
			follows = []string{u}
		}
	}

	kind := nostr.KindTextNote
	if article {
		kind = nostr.KindArticle
	}
	// get timeline
	filters := nostr.Filters{
		{
			Kinds:   []int{kind},
			Authors: follows,
			Limit:   n,
		},
	}

	// Collect all events, then sort and display top n
	events := []*nostr.Event{}
	cfg.StreamEvents(filters, true, func(ev *nostr.Event) bool {
		events = append(events, ev)
		return true
	})

	// Sort by timestamp descending (newest first)
	sort.Slice(events, func(i, j int) bool {
		return events[j].CreatedAt.Time().After(events[i].CreatedAt.Time())
	})

	if len(events) > n {
		events = events[len(events)-n:]
	}

	// Display only top n events
	for _, ev := range events {
		cfg.PrintEvent(ev, j, extra)
	}

	return nil
}

func postMsg(cCtx *cli.Context, msg string) error {
	cfg := cCtx.App.Metadata["config"].(*Config)

	var sk string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
	} else {
		return err
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
	clientTag(&ev)
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
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

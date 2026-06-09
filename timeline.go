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
	"time"

	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/sdk"
)

var usageError = errors.New("usage")

const (
	eventAuthorLookupTimeout = 2 * time.Second
	likePublishTimeout       = 3 * time.Second
)

// firstRelayHint returns the first non-empty URL from hints, or fallback if none.
func firstRelayHint(hints []string, fallback string) string {
	for _, h := range hints {
		if h != "" {
			return h
		}
	}
	return fallback
}

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
		ctx:            cCtx.Context,
		cfg:            cCtx.App.Metadata["config"].(*Config),
		content:        content,
		sensitive:      cCtx.String("sensitive"),
		geohash:        cCtx.String("geohash"),
		articleName:    articleName,
		articleTitle:   articleTitle,
		articleSummary: articleSummary,
		emoji:          cCtx.StringSlice("emoji"),
		us:             cCtx.StringSlice("u"),
		tags:           cCtx.StringSlice("tag"),
		createdAt:      nostr.Timestamp(cCtx.Int64("created-at")),
	})
}

type postArg struct {
	ctx            context.Context
	cfg            *Config
	content        string
	sensitive      string
	geohash        string
	articleName    string
	articleTitle   string
	articleSummary string
	emoji          []string
	us             []string
	tags           []string
	createdAt      nostr.Timestamp
}

func callPost(arg *postArg) error {
	sk, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return err
	}

	mentionPubkeys, err := resolveMentions(context.TODO(), arg.us)
	if err != nil {
		return err
	}

	createdAt := arg.createdAt
	if createdAt == 0 {
		createdAt = nostr.Now()
	}
	ev, err := buildPostEvent(arg, pub, mentionPubkeys, createdAt)
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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
	cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
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
	if !stdin && cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}

	cfg := cCtx.App.Metadata["config"].(*Config)
	sk, pub, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}

	var hints []string
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
		hints = evp.Relays
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
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

	ev, err := buildReplyEvent(pub, replyOpts{
		Content:   content,
		ReplyToID: id,
		RelayHint: firstRelayHint(hints, firstWriteRelay(cfg)),
		Quote:     cCtx.Bool("quote"),
		Sensitive: cCtx.String("sensitive"),
		Geohash:   cCtx.String("geohash"),
		Emojis:    cCtx.StringSlice("emoji"),
	}, cfg.Emojis, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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
	return callRepost(&repostArg{
		ctx: cCtx.Context,
		cfg: cCtx.App.Metadata["config"].(*Config),
		id:  cCtx.String("id"),
	})
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
	cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
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

	ev, err := buildDeleteEvent(pub, repostID, nostr.KindRepost, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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
		ctx:     cCtx.Context,
		cfg:     cCtx.App.Metadata["config"].(*Config),
		id:      cCtx.String("id"),
		content: cCtx.String("content"),
		emoji:   cCtx.String("emoji"),
	})
}

type likeArg struct {
	ctx     context.Context
	cfg     *Config
	id      string
	content string
	emoji   string
}

func callLike(arg *likeArg) error {
	sk, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return err
	}

	var hints []string
	var author string
	if evp := sdk.InputToEventPointer(arg.id); evp != nil {
		arg.id = evp.ID
		hints = evp.Relays
		author = evp.Author
	} else {
		return fmt.Errorf("failed to parse event from '%s'", arg.id)
	}

	mentionedPubkeys := []string{}
	if nostr.IsValidPublicKey(author) {
		mentionedPubkeys = append(mentionedPubkeys, author)
	} else if relayHint := firstRelayHint(hints, ""); relayHint != "" {
		mentionedPubkeys = fetchEventAuthorHintsFromRelay(arg.ctx, relayHint, nostr.Filter{
			Kinds: []int{nostr.KindTextNote},
			IDs:   []string{arg.id},
			Limit: 1,
		})
	}

	ev, err := buildLikeEvent(pub, arg.id, firstRelayHint(hints, ""), arg.content, arg.emoji, mentionedPubkeys, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	ctx, cancel := context.WithTimeout(arg.ctx, likePublishTimeout)
	defer cancel()
	arg.cfg.Do(ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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

// fetchEventAuthorHints returns authors found from the first responding write relay.
func fetchEventAuthorHints(ctx context.Context, cfg *Config, filter nostr.Filter) []string {
	var ids []string
	var first atomic.Bool
	first.Store(true)
	cfg.Do(ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		if !first.CompareAndSwap(true, false) {
			return true
		}
		evs, err := relay.QuerySync(ctx, filter)
		if err != nil {
			return true
		}
		for _, tmp := range evs {
			ids = append(ids, tmp.PubKey)
		}
		return true
	})
	return ids
}

func fetchEventAuthorHintsFromRelay(ctx context.Context, relayURL string, filter nostr.Filter) []string {
	ctx, cancel := context.WithTimeout(ctx, eventAuthorLookupTimeout)
	defer cancel()

	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return nil
	}
	defer relay.Close()

	evs, err := relay.QuerySync(ctx, filter)
	if err != nil {
		return nil
	}
	authors := make([]string, 0, len(evs))
	for _, ev := range evs {
		authors = append(authors, ev.PubKey)
	}
	return authors
}

func doUnlike(cCtx *cli.Context) error {
	return callUnlike(&unlikeArg{
		ctx: cCtx.Context,
		cfg: cCtx.App.Metadata["config"].(*Config),
		id:  cCtx.String("id"),
	})
}

type unlikeArg struct {
	ctx context.Context
	cfg *Config
	id  string
}

func callUnlike(arg *unlikeArg) error {
	id := arg.id
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
	} else {
		return fmt.Errorf("failed to parse event from '%s'", arg.id)
	}

	sk, pub, err := getSkAndPub(arg.cfg)
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
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
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

	ev, err := buildDeleteEvent(pub, likeID, nostr.KindReaction, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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

type getEventArg struct {
	ctx context.Context
	cfg *Config
	id  string
}

func callGetEvent(arg *getEventArg) (*nostr.Event, error) {
	id := arg.id
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
	} else {
		return nil, fmt.Errorf("failed to parse event from '%s'", arg.id)
	}
	evs, err := arg.cfg.QueryEvents(arg.ctx, nostr.Filters{{IDs: []string{id}, Limit: 1}})
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		return nil, fmt.Errorf("event not found: %s", arg.id)
	}
	return evs[0], nil
}

func doDelete(cCtx *cli.Context) error {
	return callDelete(&deleteArg{
		ctx: cCtx.Context,
		cfg: cCtx.App.Metadata["config"].(*Config),
		id:  cCtx.String("id"),
	})
}

func doSearch(cCtx *cli.Context) error {
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)
	evs, err := callSearch(&searchArg{
		cfg:    cfg,
		search: strings.Join(cCtx.Args().Slice(), " "),
		n:      cCtx.Int("n"),
	})
	if err != nil {
		return err
	}
	cfg.PrintEvents(evs, nil, j, extra)
	return nil
}

type searchArg struct {
	ctx    context.Context
	cfg    *Config
	search string
	n      int
}

func callSearch(arg *searchArg) ([]*nostr.Event, error) {
	// get timeline
	filters := nostr.Filters{
		{
			Kinds:  []int{nostr.KindTextNote},
			Search: arg.search,
			Limit:  arg.n,
		},
	}

	return arg.cfg.QueryEvents(arg.ctx, filters)
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
		cfg.Do(context.Background(), Relay{Read: true}, func(ctx context.Context, relay *nostr.Relay) bool {
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
	cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
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
		if len(authors) > 0 {
			for _, author := range authors {
				if pp := sdk.InputToProfile(context.TODO(), author); pp != nil {
					follows = append(follows, pp.PublicKey)
				} else {
					return fmt.Errorf("failed to parse pubkey from '%s'", author)
				}
			}
		} else {
			follows = append(follows, cfg.FollowList...)
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
			match := re.FindStringSubmatchIndex(ev.Content)
			if match == nil {
				continue
			}
			evr.Content = string(re.ExpandString(nil, reply, ev.Content, match))
			evr.Tags = nostr.Tags{}
			clientTag(&evr)
			for _, tag := range ev.Tags {
				if len(tag) > 0 && tag[0] != "e" && tag[0] != "p" {
					evr.Tags = evr.Tags.AppendUnique(tag)
				}
			}
			evr.Tags = evr.Tags.AppendUnique(nostr.Tag{"e", ev.ID, "", "reply"})
			evr.CreatedAt = ev.CreatedAt + 1
			evr.Kind = ev.Kind
			if err := evr.Sign(sk); err != nil {
				return err
			}
			cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
				relay.Publish(ctx, evr)
				return true
			})
		}
	}

	return nil
}

func doTimeline(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	events, err := callTimeline(&timelineArg{
		cfg:     cfg,
		global:  cCtx.Bool("global"),
		u:       cCtx.String("u"),
		n:       cCtx.Int("n"),
		article: cCtx.Bool("article"),
	})

	if err != nil {
		return err
	}

	// Display only top n events
	for _, ev := range events {
		cfg.PrintEvent(ev, cCtx.Bool("json"), cCtx.Bool("extra"))
	}

	return nil
}

type timelineArg struct {
	ctx     context.Context
	cfg     *Config
	global  bool
	u       string
	n       int
	j       bool
	extra   bool
	article bool
}

func callTimeline(arg *timelineArg) ([]*nostr.Event, error) {
	var follows []string
	if arg.global {
		follows = nil
	} else {
		if arg.u == "" {
			follows = append(follows, arg.cfg.FollowList...)
			if len(follows) == 0 {
				return nil, fmt.Errorf("no follows found. Please follow someone first.")
			}
		} else {
			if pp := sdk.InputToProfile(context.TODO(), arg.u); pp != nil {
				arg.u = pp.PublicKey
			} else {
				return nil, fmt.Errorf("failed to parse pubkey from '%s'", arg.u)
			}
			follows = []string{arg.u}
		}
	}

	kind := nostr.KindTextNote
	if arg.article {
		kind = nostr.KindArticle
	}
	// get timeline
	filters := nostr.Filters{
		{
			Kinds:   []int{kind},
			Authors: follows,
			Limit:   arg.n,
		},
	}

	// Collect all events
	events := []*nostr.Event{}
	arg.cfg.StreamEvents(filters, true, func(ev *nostr.Event) bool {
		events = append(events, ev)
		return true
	})

	// Sort by timestamp descending (newest last)
	sort.Slice(events, func(i, j int) bool {
		return events[j].CreatedAt.Time().After(events[i].CreatedAt.Time())
	})

	if len(events) > arg.n {
		events = events[len(events)-arg.n:]
	}

	return events, nil
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
	cfg.Do(context.Background(), Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
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

type replyArg struct {
	ctx     context.Context
	cfg     *Config
	id      string
	content string
}

func callReply(arg *replyArg) error {
	id := arg.id
	var hints []string
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
		hints = evp.Relays
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
	}

	sk, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return err
	}

	ev, err := buildReplyEvent(pub, replyOpts{
		Content:   arg.content,
		ReplyToID: id,
		RelayHint: firstRelayHint(hints, firstWriteRelay(arg.cfg)),
	}, arg.cfg.Emojis, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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

type repostArg struct {
	ctx context.Context
	cfg *Config
	id  string
}

func callRepost(arg *repostArg) error {
	id := arg.id

	sk, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return err
	}

	var hints []string
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
		hints = evp.Relays
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
	}

	filter := nostr.Filter{
		Kinds: []int{nostr.KindTextNote},
		IDs:   []string{id},
	}
	mentionedPubkeys := fetchEventAuthorHints(arg.ctx, arg.cfg, filter)

	ev, err := buildRepostEvent(pub, id, firstRelayHint(hints, firstWriteRelay(arg.cfg)), mentionedPubkeys, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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

type unrepostArg struct {
	ctx context.Context
	cfg *Config
	id  string
}

func callUnrepost(arg *unrepostArg) error {
	id := arg.id
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
	}

	sk, pub, err := getSkAndPub(arg.cfg)
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
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
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

	ev, err := buildDeleteEvent(pub, repostID, nostr.KindRepost, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
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

type deleteArg struct {
	ctx context.Context
	cfg *Config
	id  string
}

func callDelete(arg *deleteArg) error {
	id := arg.id
	var hints []string
	var kind int
	if evp := sdk.InputToEventPointer(id); evp != nil {
		id = evp.ID
		hints = evp.Relays
		kind = evp.Kind
	} else {
		return fmt.Errorf("failed to parse event from '%s'", id)
	}

	sk, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return err
	}

	ev, err := buildDeleteEvent(pub, id, kind, nostr.Now())
	if err != nil {
		return err
	}
	if err := ev.Sign(sk); err != nil {
		return err
	}

	var success atomic.Int64
	arg.cfg.Do(arg.ctx, Relay{Write: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		err := relay.Publish(ctx, *ev)
		if err != nil {
			fmt.Fprintln(os.Stderr, relay.URL, err)
		} else {
			success.Add(1)
		}
		return true
	})

	// Also publish to relay hints from the nevent that aren't already in cfg.Relays.
	for _, url := range hints {
		if _, ok := arg.cfg.Relays[url]; ok {
			continue
		}
		relay, err := nostr.RelayConnect(arg.ctx, url)
		if err != nil {
			fmt.Fprintln(os.Stderr, url, err)
			continue
		}
		if err := relay.Publish(arg.ctx, *ev); err != nil {
			fmt.Fprintln(os.Stderr, url, err)
		} else {
			success.Add(1)
		}
		relay.Close()
	}

	if success.Load() == 0 {
		return errors.New("cannot delete")
	}
	return nil
}

func formatTimelineForView(events []*nostr.Event, cfg *Config) string {
	var sb strings.Builder

	for i, ev := range events {
		// Get author profile
		npub, err := nip19.EncodePublicKey(ev.PubKey)
		authorName := npub
		if err == nil {
			profile, err := cfg.GetProfile(npub)
			if err == nil {
				if profile.DisplayName != "" {
					authorName = profile.DisplayName
				} else if profile.Name != "" {
					authorName = profile.Name
				}
			}
		}

		// Format post
		sb.WriteString(fmt.Sprintf("#%d @%s\n", i+1, authorName))
		sb.WriteString(ev.Content)
		sb.WriteString(fmt.Sprintf("\n[ID: %s]\n\n", ev.ID))
	}

	return sb.String()
}

func doCat(cCtx *cli.Context) error {
	j := cCtx.Bool("json")
	extra := cCtx.Bool("extra")

	cfg := cCtx.App.Metadata["config"].(*Config)

	dec := json.NewDecoder(os.Stdin)
	for dec.More() {
		var ev nostr.Event
		if err := dec.Decode(&ev); err != nil {
			return err
		}
		cfg.PrintEvent(&ev, j, extra)
	}
	return nil
}

type reportArg struct {
	ctx        context.Context
	cfg        *Config
	id         string
	reportType string
}

func callReport(arg *reportArg) error {
	sk := arg.cfg.sk
	if sk == "" {
		if _, s, err := nip19.Decode(arg.cfg.PrivateKey); err == nil {
			sk = s.(string)
		} else {
			return err
		}
	}
	if sk == "" {
		return fmt.Errorf("no private key found")
	}

	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return err
	}

	targetID := arg.id
	var targetPubkey string
	var targetEventID string

	// Decode the target ID
	if _, decoded, err := nip19.Decode(targetID); err == nil {
		if decoded == nil {
			return fmt.Errorf("invalid target format")
		}

		switch decoded.(type) {
		case string:
			targetPubkey = decoded.(string)
		case map[string]interface{}:
			if e, ok := decoded.(map[string]interface{})["e"]; ok {
				if eStr, ok := e.(string); ok {
					targetEventID = eStr
				}
			}
			if p, ok := decoded.(map[string]interface{})["p"]; ok {
				if pStr, ok := p.(string); ok {
					targetPubkey = pStr
				}
			}
		default:
			return fmt.Errorf("unsupported target format")
		}
	} else {
		if prefix, decoded, err := nip19.Decode(targetID); err == nil && prefix == "npub" {
			if pubkey, ok := decoded.(string); ok {
				targetPubkey = pubkey
			}
		} else {
			targetEventID = targetID
		}
	}

	report := &nostr.Event{
		PubKey:    pub,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Kind:      1984,
		Tags: nostr.Tags{
			nostr.Tag{"d", arg.reportType},
		},
		Content: fmt.Sprintf("Reported: %s", arg.reportType),
	}

	if targetPubkey != "" {
		report.Tags = append(report.Tags, nostr.Tag{"p", targetPubkey})
	}
	if targetEventID != "" {
		report.Tags = append(report.Tags, nostr.Tag{"e", targetEventID})
	}

	if err := report.Sign(sk); err != nil {
		return err
	}

	if arg.cfg.verbose {
		fmt.Printf("Created report event kind 1984\n")
		fmt.Printf("Report type: %s\n", arg.reportType)
		if targetPubkey != "" {
			fmt.Printf("Target pubkey: %s\n", targetPubkey)
		}
		if targetEventID != "" {
			fmt.Printf("Target event: %s\n", targetEventID)
		}
	}

	relays := []string{}
	for k, v := range arg.cfg.Relays {
		if v.Write {
			relays = append(relays, k)
		}
	}

	if len(relays) == 0 {
		return fmt.Errorf("no write relays available")
	}

	ctx, cancel := context.WithTimeout(arg.ctx, 10*time.Second)
	defer cancel()

	for _, relayURL := range relays {
		relay, err := nostr.RelayConnect(ctx, relayURL)
		if err != nil {
			if arg.cfg.verbose {
				fmt.Fprintf(os.Stderr, "Failed to connect to relay %s: %v\n", relayURL, err)
			}
			continue
		}
		defer relay.Close()

		err = relay.Publish(ctx, *report)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to publish to relay %s: %v\n", relayURL, err)
		} else {
			fmt.Printf("Report sent to %s\n", relayURL)
		}
	}

	return nil
}

type reportProfileArg struct {
	ctx        context.Context
	cfg        *Config
	user       string
	reportType string
}

func callReportProfile(arg *reportProfileArg) error {
	sk := arg.cfg.sk
	if sk == "" {
		if _, s, err := nip19.Decode(arg.cfg.PrivateKey); err == nil {
			sk = s.(string)
		} else {
			return err
		}
	}
	if sk == "" {
		return fmt.Errorf("no private key found")
	}

	pub, err := nostr.GetPublicKey(sk)
	if err != nil {
		return err
	}

	var targetPubkey string

	// Decode the user ID
	if _, decoded, err := nip19.Decode(arg.user); err == nil {
		if decoded == nil {
			return fmt.Errorf("invalid target format")
		}

		switch decoded.(type) {
		case string:
			targetPubkey = decoded.(string)
		case map[string]interface{}:
			if p, ok := decoded.(map[string]interface{})["p"]; ok {
				if pStr, ok := p.(string); ok {
					targetPubkey = pStr
				}
			}
		default:
			return fmt.Errorf("unsupported target format")
		}
	} else {
		if prefix, decoded, err := nip19.Decode(arg.user); err == nil && prefix == "npub" {
			if pubkey, ok := decoded.(string); ok {
				targetPubkey = pubkey
			} else {
				return fmt.Errorf("invalid npub format")
			}
		} else {
			// Assume it's a hex public key
			targetPubkey = arg.user
		}
	}

	report := &nostr.Event{
		PubKey:    pub,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Kind:      1984,
		Tags: nostr.Tags{
			nostr.Tag{"d", arg.reportType},
		},
		Content: fmt.Sprintf("Reported: %s", arg.reportType),
	}

	if targetPubkey != "" {
		report.Tags = append(report.Tags, nostr.Tag{"p", targetPubkey})
	}

	if err := report.Sign(sk); err != nil {
		return err
	}

	if arg.cfg.verbose {
		fmt.Printf("Created report event kind 1984\n")
		fmt.Printf("Report type: %s\n", arg.reportType)
		fmt.Printf("Target pubkey: %s\n", targetPubkey)
	}

	relays := []string{}
	for k, v := range arg.cfg.Relays {
		if v.Write {
			relays = append(relays, k)
		}
	}

	if len(relays) == 0 {
		return fmt.Errorf("no write relays available")
	}

	ctx, cancel := context.WithTimeout(arg.ctx, 10*time.Second)
	defer cancel()

	for _, relayURL := range relays {
		relay, err := nostr.RelayConnect(ctx, relayURL)
		if err != nil {
			if arg.cfg.verbose {
				fmt.Fprintf(os.Stderr, "Failed to connect to relay %s: %v\n", relayURL, err)
			}
			continue
		}
		defer relay.Close()

		err = relay.Publish(ctx, *report)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to publish to relay %s: %v\n", relayURL, err)
		} else {
			fmt.Printf("Report sent to %s\n", relayURL)
		}
	}

	return nil
}

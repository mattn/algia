package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/urfave/cli/v2"

	"github.com/fatih/color"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip04"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const name = "algia"

const version = "0.0.70"

var revision = "HEAD"

// Relay is
type Relay struct {
	Read   bool `json:"read"`
	Write  bool `json:"write"`
	Search bool `json:"search"`
}

// Config is
type Config struct {
	Relays     map[string]Relay   `json:"relays"`
	Follows    map[string]Profile `json:"follows"`
	PrivateKey string             `json:"privatekey"`
	Updated    time.Time          `json:"updated"`
	Emojis     map[string]string  `json:"emojis"`
	NwcURI     string             `json:"nwc-uri"`
	NwcPub     string             `json:"nwc-pub"`
	verbose    bool
	tempRelay  bool
	sk         string
}

// Event is
type Event struct {
	Event   *nostr.Event `json:"event"`
	Profile Profile      `json:"profile"`
}

// Profile is
type Profile struct {
	Website     string `json:"website"`
	Nip05       string `json:"nip05"`
	Picture     string `json:"picture"`
	Lud16       string `json:"lud16"`
	DisplayName string `json:"display_name"`
	About       string `json:"about"`
	Name        string `json:"name"`
	Bot         bool   `json:"bot"`
}

func configDir() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		dir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, ".config"), nil
	default:
		return os.UserConfigDir()
	}
}

func loadConfig(profile string) (*Config, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "algia")

	var fp string
	if profile == "" {
		fp = filepath.Join(dir, "config.json")
	} else if profile == "?" {
		names, err := filepath.Glob(filepath.Join(dir, "config-*.json"))
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			name = filepath.Base(name)
			name = strings.TrimLeft(name[6:len(name)-5], "-")
			fmt.Println(name)
		}
		os.Exit(0)
	} else {
		fp = filepath.Join(dir, "config-"+profile+".json")
	}
	os.MkdirAll(filepath.Dir(fp), 0700)

	b, err := os.ReadFile(fp)
	if err != nil {
		return nil, err
	}
	var cfg Config
	err = json.Unmarshal(b, &cfg)
	if err != nil {
		return nil, err
	}
	if len(cfg.Relays) == 0 {
		cfg.Relays = map[string]Relay{}
		cfg.Relays["wss://relay.nostr.band"] = Relay{
			Read:   true,
			Write:  true,
			Search: true,
		}
	}
	return &cfg, nil
}

// GetFollows is
func (cfg *Config) GetFollows(profile string) (map[string]Profile, error) {
	var mu sync.Mutex
	var pub string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		if pub, err = nostr.GetPublicKey(s.(string)); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	// get followers
	if (cfg.Updated.Add(3*time.Hour).Before(time.Now()) && !cfg.tempRelay) || len(cfg.Follows) == 0 {
		mu.Lock()
		cfg.Follows = map[string]Profile{}
		mu.Unlock()
		m := map[string]struct{}{}

		cfg.Do(Relay{Read: true}, func(ctx context.Context, relay *nostr.Relay) bool {
			evs, err := relay.QuerySync(ctx, nostr.Filter{Kinds: []int{nostr.KindContactList}, Authors: []string{pub}, Limit: 1})
			if err != nil {
				return true
			}
			for _, ev := range evs {
				var rm map[string]Relay
				if cfg.tempRelay == false {
					if err := json.Unmarshal([]byte(ev.Content), &rm); err == nil {
						for k, v1 := range cfg.Relays {
							if v2, ok := rm[k]; ok {
								v2.Search = v1.Search
							}
						}
						cfg.Relays = rm
					}
				}
				for _, tag := range ev.Tags {
					if len(tag) >= 2 && tag[0] == "p" {
						mu.Lock()
						m[tag[1]] = struct{}{}
						mu.Unlock()
					}
				}
			}
			return true
		})
		if cfg.verbose {
			fmt.Printf("found %d followers\n", len(m))
		}
		if len(m) > 0 {
			follows := []string{}
			for k := range m {
				follows = append(follows, k)
			}

			for i := 0; i < len(follows); i += 500 {
				// Calculate the end index based on the current index and slice length
				end := i + 500
				if end > len(follows) {
					end = len(follows)
				}

				// get follower's descriptions
				cfg.Do(Relay{Read: true}, func(ctx context.Context, relay *nostr.Relay) bool {
					evs, err := relay.QuerySync(ctx, nostr.Filter{
						Kinds:   []int{nostr.KindProfileMetadata},
						Authors: follows[i:end], // Use the updated end index
					})
					if err != nil {
						return true
					}
					for _, ev := range evs {
						var profile Profile
						err := json.Unmarshal([]byte(ev.Content), &profile)
						if err == nil {
							mu.Lock()
							cfg.Follows[ev.PubKey] = profile
							mu.Unlock()
						}
					}
					return true
				})
			}
		}

		cfg.Updated = time.Now()
		if err := cfg.save(profile); err != nil {
			return nil, err
		}
	}
	return cfg.Follows, nil
}

// FindRelay is
func (cfg *Config) FindRelay(ctx context.Context, r Relay) *nostr.Relay {
	for k, v := range cfg.Relays {
		if r.Write && !v.Write {
			continue
		}
		if !cfg.tempRelay && r.Search && !v.Search {
			continue
		}
		if !r.Write && !v.Read {
			continue
		}
		if cfg.verbose {
			fmt.Printf("trying relay: %s\n", k)
		}
		relay, err := nostr.RelayConnect(ctx, k)
		if err != nil {
			if cfg.verbose {
				fmt.Fprintln(os.Stderr, err.Error())
			}
			continue
		}
		return relay
	}
	return nil
}

// Do is
func (cfg *Config) Do(r Relay, f func(context.Context, *nostr.Relay) bool) {
	var wg sync.WaitGroup
	ctx := context.Background()
	for k, v := range cfg.Relays {
		if r.Write && !v.Write {
			continue
		}
		if r.Search && !v.Search {
			continue
		}
		if !r.Write && !v.Read {
			continue
		}
		wg.Add(1)
		go func(wg *sync.WaitGroup, k string, v Relay) {
			defer wg.Done()
			relay, err := nostr.RelayConnect(ctx, k)
			if err != nil {
				if cfg.verbose {
					fmt.Fprintln(os.Stderr, err)
				}
				return
			}
			if !f(ctx, relay) {
				ctx.Done()
			}
			relay.Close()
		}(&wg, k, v)
	}
	wg.Wait()
}

func (cfg *Config) save(profile string) error {
	if cfg.tempRelay {
		return nil
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "algia")

	var fp string
	if profile == "" {
		fp = filepath.Join(dir, "config.json")
	} else {
		fp = filepath.Join(dir, "config-"+profile+".json")
	}
	b, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fp, b, 0644)
}

// Decode is
func (cfg *Config) Decode(ev *nostr.Event) error {
	var sk string
	var pub string
	if _, s, err := nip19.Decode(cfg.PrivateKey); err == nil {
		sk = s.(string)
		if pub, err = nostr.GetPublicKey(s.(string)); err != nil {
			return err
		}
	} else {
		return err
	}
	tag := ev.Tags.GetFirst([]string{"p"})
	sp := pub
	if tag != nil {
		sp = tag.Value()
		if sp != pub {
			if ev.PubKey != pub {
				return errors.New("is not author")
			}
		} else {
			sp = ev.PubKey
		}
	}
	ss, err := nip04.ComputeSharedSecret(sp, sk)
	if err != nil {
		return err
	}
	content, err := nip04.Decrypt(ev.Content, ss)
	if err != nil {
		return err
	}
	ev.Content = content
	return nil
}

// PrintEvents is
func (cfg *Config) PrintEvents(evs []*nostr.Event, followsMap map[string]Profile, j, extra bool) {
	if j {
		if extra {
			var events []Event
			for _, ev := range evs {
				if profile, ok := followsMap[ev.PubKey]; ok {
					events = append(events, Event{
						Event:   ev,
						Profile: profile,
					})
				}
			}
			for _, ev := range events {
				json.NewEncoder(os.Stdout).Encode(ev)
			}
		} else {
			for _, ev := range evs {
				json.NewEncoder(os.Stdout).Encode(ev)
			}
		}
		return
	}

	for _, ev := range evs {
		profile, ok := followsMap[ev.PubKey]
		if ok {
			color.Set(color.FgHiRed)
			fmt.Print(profile.Name)
		} else {
			color.Set(color.FgRed)
			if pk, err := nip19.EncodePublicKey(ev.PubKey); err == nil {
				fmt.Print(pk)
			} else {
				fmt.Print(ev.PubKey)
			}
		}
		color.Set(color.Reset)
		fmt.Print(": ")
		color.Set(color.FgHiBlue)
		if ni, err := nip19.EncodeNote(ev.ID); err == nil {
			fmt.Println(ni)
		} else {
			fmt.Println(ev.ID)
		}
		color.Set(color.Reset)
		fmt.Println(ev.Content)
	}
}

// Events is
func (cfg *Config) Events(filter nostr.Filter) []*nostr.Event {
	var mu sync.Mutex
	found := false
	var m sync.Map
	cfg.Do(Relay{Read: true}, func(ctx context.Context, relay *nostr.Relay) bool {
		mu.Lock()
		if found {
			mu.Unlock()
			return false
		}
		mu.Unlock()
		evs, err := relay.QuerySync(ctx, filter)
		if err != nil {
			return true
		}
		for _, ev := range evs {
			if _, ok := m.Load(ev.ID); !ok {
				if ev.Kind == nostr.KindEncryptedDirectMessage || ev.Kind == nostr.KindCategorizedBookmarksList {
					if err := cfg.Decode(ev); err != nil {
						continue
					}
				}
				m.LoadOrStore(ev.ID, ev)
				if len(filter.IDs) == 1 {
					mu.Lock()
					found = true
					ctx.Done()
					mu.Unlock()
					break
				}
			}
		}
		return true
	})

	keys := []string{}
	m.Range(func(k, v any) bool {
		keys = append(keys, k.(string))
		return true
	})
	sort.Slice(keys, func(i, j int) bool {
		lhs, ok := m.Load(keys[i])
		if !ok {
			return false
		}
		rhs, ok := m.Load(keys[j])
		if !ok {
			return false
		}
		return lhs.(*nostr.Event).CreatedAt.Time().Before(rhs.(*nostr.Event).CreatedAt.Time())
	})
	var evs []*nostr.Event
	for _, key := range keys {
		vv, ok := m.Load(key)
		if !ok {
			continue
		}
		evs = append(evs, vv.(*nostr.Event))
	}
	return evs
}

func doVersion(cCtx *cli.Context) error {
	fmt.Println(version)
	return nil
}

func main() {
	app := &cli.App{
		Usage:       "A cli application for nostr",
		Description: "A cli application for nostr",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "a", Usage: "profile name"},
			&cli.StringFlag{Name: "relays", Usage: "relays"},
			&cli.BoolFlag{Name: "V", Usage: "verbose"},
		},
		Commands: []*cli.Command{
			{
				Name:    "timeline",
				Aliases: []string{"tl"},
				Usage:   "show timeline",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "u", Usage: "user"},
					&cli.IntFlag{Name: "n", Value: 30, Usage: "number of items"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.BoolFlag{Name: "extra", Usage: "extra JSON"},
					&cli.BoolFlag{Name: "article", Usage: "show articles"},
				},
				Action: doTimeline,
			},
			{
				Name:  "stream",
				Usage: "show stream",
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "author"},
					&cli.IntSliceFlag{Name: "kind", Value: cli.NewIntSlice(nostr.KindTextNote)},
					&cli.BoolFlag{Name: "follow"},
					&cli.StringFlag{Name: "pattern"},
					&cli.StringFlag{Name: "reply"},
				},
				Action: doStream,
			},
			{
				Name:    "post",
				Aliases: []string{"n"},
				Flags: []cli.Flag{
					&cli.StringSliceFlag{Name: "u", Usage: "users"},
					&cli.BoolFlag{Name: "stdin"},
					&cli.StringFlag{Name: "sensitive"},
					&cli.StringSliceFlag{Name: "emoji"},
					&cli.StringFlag{Name: "geohash"},
					&cli.StringFlag{Name: "article-name"},
					&cli.StringFlag{Name: "article-title"},
					&cli.StringFlag{Name: "article-summary"},
				},
				Usage:     "post new note",
				UsageText: "algia post [note text]",
				HelpName:  "post",
				ArgsUsage: "[note text]",
				Action:    doPost,
			},
			{
				Name:    "reply",
				Aliases: []string{"r"},
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "stdin"},
					&cli.StringFlag{Name: "id", Required: true},
					&cli.BoolFlag{Name: "quote"},
					&cli.StringFlag{Name: "sensitive"},
					&cli.StringSliceFlag{Name: "emoji"},
					&cli.StringFlag{Name: "geohash"},
				},
				Usage:     "reply to the note",
				UsageText: "algia reply --id [id] [note text]",
				HelpName:  "reply",
				ArgsUsage: "[note text]",
				Action:    doReply,
			},
			{
				Name:    "repost",
				Aliases: []string{"b"},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
				},
				Usage:     "repost the note",
				UsageText: "algia repost --id [id]",
				HelpName:  "repost",
				Action:    doRepost,
			},
			{
				Name:    "unrepost",
				Aliases: []string{"B"},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
				},
				Usage:     "unrepost the note",
				UsageText: "algia unrepost --id [id]",
				HelpName:  "unrepost",
				Action:    doUnrepost,
			},
			{
				Name:    "like",
				Aliases: []string{"l"},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
					&cli.StringFlag{Name: "content"},
					&cli.StringFlag{Name: "emoji"},
				},
				Usage:     "like the note",
				UsageText: "algia like --id [id]",
				HelpName:  "like",
				Action:    doLike,
			},
			{
				Name:    "unlike",
				Aliases: []string{"L"},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
				},
				Usage:     "unlike the note",
				UsageText: "algia unlike --id [id]",
				HelpName:  "unlike",
				Action:    doUnlike,
			},
			{
				Name:    "delete",
				Aliases: []string{"d"},
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
				},
				Usage:     "delete the note",
				UsageText: "algia delete --id [id]",
				HelpName:  "delete",
				Action:    doDelete,
			},
			{
				Name:    "search",
				Aliases: []string{"s"},
				Flags: []cli.Flag{
					&cli.IntFlag{Name: "n", Value: 30, Usage: "number of items"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.BoolFlag{Name: "extra", Usage: "extra JSON"},
				},
				Usage:     "search notes",
				UsageText: "algia search [words]",
				HelpName:  "search",
				Action:    doSearch,
			},
			{
				Name: "broadcast",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "id", Required: true},
					&cli.StringFlag{Name: "relay", Required: false},
				},
				Usage:     "broadcast the note",
				UsageText: "algia broadcast --id [id]",
				HelpName:  "broadcast",
				Action:    doBroadcast,
			},
			{
				Name: "dm-list",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
				},
				Usage:     "show DM list",
				UsageText: "algia dm-list",
				HelpName:  "dm-list",
				Action:    doDMList,
			},
			{
				Name: "dm-timeline",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "u", Value: "", Usage: "DM user", Required: true},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.BoolFlag{Name: "extra", Usage: "extra JSON"},
				},
				Usage:     "show DM timeline",
				UsageText: "algia dm-timeline",
				HelpName:  "dm-timeline",
				Action:    doDMTimeline,
			},
			{
				Name: "dm-post",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "u", Value: "", Usage: "DM user", Required: true},
					&cli.BoolFlag{Name: "stdin"},
					&cli.StringFlag{Name: "sensitive"},
				},
				Usage:     "post new DM note",
				UsageText: "algia post [note text]",
				HelpName:  "post",
				ArgsUsage: "[note text]",
				Action:    doDMPost,
			},
			{
				Name: "bm-list",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
				},
				Usage:     "show bookmarks",
				UsageText: "algia bm-list",
				HelpName:  "bm-list",
				Action:    doBMList,
			},
			{
				Name:      "bm-post",
				Usage:     "post bookmark",
				UsageText: "algia bm-post [note]",
				HelpName:  "bm-post",
				ArgsUsage: "[note]",
				Action:    doBMPost,
			},
			{
				Name: "profile",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "u", Value: "", Usage: "user"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
					&cli.StringSliceFlag{Name: "set", Usage: "set attributes"},
				},
				Usage:     "show profile",
				UsageText: "algia profile",
				HelpName:  "profile",
				Action:    doProfile,
			},
			{
				Name:      "update-profile",
				Usage:     "update profile",
				UsageText: "algia update-profile",
				HelpName:  "update-profile",
				Action:    doUpdateProfile,
			},
			{
				Name:      "powa",
				Usage:     "post ぽわ〜",
				UsageText: "algia powa",
				HelpName:  "powa",
				Action:    doPowa,
			},
			{
				Name:      "puru",
				Usage:     "post ぷる",
				UsageText: "algia puru",
				HelpName:  "puru",
				Action:    doPuru,
			},
			{
				Name: "zap",
				Flags: []cli.Flag{
					&cli.Uint64Flag{Name: "amount", Usage: "amount for zap", Value: 1},
					&cli.StringFlag{Name: "comment", Usage: "comment for zap", Value: ""},
				},
				Usage:     "zap something",
				UsageText: "algia zap [note|npub|nevent]",
				HelpName:  "zap",
				Action:    doZap,
			},
			{
				Name:      "version",
				Usage:     "show version",
				UsageText: "algia version",
				HelpName:  "version",
				Action:    doVersion,
			},
		},
		Before: func(cCtx *cli.Context) error {
			if cCtx.Args().Get(0) == "version" {
				return nil
			}
			profile := cCtx.String("a")
			cfg, err := loadConfig(profile)
			if err != nil {
				return err
			}
			cCtx.App.Metadata = map[string]any{
				"config": cfg,
			}
			cfg.verbose = cCtx.Bool("V")
			relays := cCtx.String("relays")
			if strings.TrimSpace(relays) != "" {
				cfg.Relays = make(map[string]Relay)
				for _, relay := range strings.Split(relays, ",") {
					cfg.Relays[relay] = Relay{
						Read:  true,
						Write: true,
					}
				}
				cfg.tempRelay = true
			}
			return nil
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

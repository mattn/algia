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

const version = "0.0.87"

var revision = "HEAD"

// Relay is
type Relay struct {
	Read   bool `json:"read"`
	Write  bool `json:"write"`
	Search bool `json:"search"`
}

// Config is
type Config struct {
	Relays         map[string]Relay  `json:"relays"`
	FollowList     []string          `json:"followList"`
	PrivateKey     string            `json:"privatekey"`
	Updated        time.Time         `json:"updated"`
	Emojis         map[string]string `json:"emojis"`
	NwcURI         string            `json:"nwc-uri"`
	profiles       map[string]Profile
	pool           *nostr.SimplePool
	profileChanged bool
	verbose        bool
	tempRelay      bool
	sk             string
}

// Event is
type Event struct {
	Event   *nostr.Event `json:"event"`
	Profile Profile      `json:"profile"`
}

// Profile is
type Profile struct {
	Website     string    `json:"website"`
	Nip05       string    `json:"nip05"`
	Picture     string    `json:"picture"`
	Lud16       string    `json:"lud16"`
	DisplayName string    `json:"display_name"`
	About       string    `json:"about"`
	Name        string    `json:"name"`
	Bot         bool      `json:"bot"`
	FetchedAt   time.Time `json:"fetched_at,omitempty"`
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
	var profilesFp string
	if profile == "" {
		fp = filepath.Join(dir, "config.json")
		profilesFp = filepath.Join(dir, "profiles.json")
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
		profilesFp = filepath.Join(dir, "profiles-"+profile+".json")
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

	// Load profiles from profiles.json
	cfg.profiles = make(map[string]Profile)
	if profilesData, err := os.ReadFile(profilesFp); err == nil {
		json.Unmarshal(profilesData, &cfg.profiles)
	}

	if cfg.FollowList == nil {
		cfg.FollowList = []string{}
	}
	// Initialize pool with read relays
	cfg.pool = nostr.NewSimplePool(context.Background())
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
	if len(cfg.FollowList) == 0 {
		mu.Lock()
		cfg.FollowList = []string{}
		mu.Unlock()
		m := map[string]struct{}{}

		cfg.Do(Relay{Read: true}, func(ctx context.Context, relay *nostr.Relay) bool {
			if cfg.tempRelay == false {
				evs, _ := relay.QuerySync(ctx, nostr.Filter{Kinds: []int{nostr.KindRelayListMetadata}, Authors: []string{pub}, Limit: 1})
				if len(evs) > 0 {
					rm := map[string]Relay{}
					for _, r := range evs[0].Tags.GetAll([]string{"r"}) {
						if len(r) == 2 {
							rm[r[1]] = Relay{
								Read:  true,
								Write: true,
							}
						} else if len(r) == 3 {
							switch r[2] {
							case "read":
								rm[r[1]] = Relay{
									Read:  true,
									Write: false,
								}
							case "write":
								rm[r[1]] = Relay{
									Read:  true,
									Write: true,
								}
							}
						}
					}
					for k, v1 := range cfg.Relays {
						if v2, ok := rm[k]; ok {
							v2.Search = v1.Search
						}
					}
					cfg.Relays = rm
				}
			}

			evs, _ := relay.QuerySync(ctx, nostr.Filter{Kinds: []int{nostr.KindFollowList}, Authors: []string{pub}, Limit: 1})
			if len(evs) > 0 {
				for _, tag := range evs[0].Tags {
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

			// Store follows as array of pubkeys
			mu.Lock()
			cfg.FollowList = follows
			mu.Unlock()

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
							profile.FetchedAt = time.Now()
							mu.Lock()
							cfg.profiles[ev.PubKey] = profile
							cfg.profileChanged = true
							mu.Unlock()
						}
					}
					return true
				})
			}
		}

		if err := cfg.save(profile); err != nil {
			return nil, err
		}
	}

	// Return a map for compatibility with existing code
	followsMap := make(map[string]Profile)
	for _, pubkey := range cfg.FollowList {
		if profile, ok := cfg.profiles[pubkey]; ok {
			followsMap[pubkey] = profile
		}
	}
	return followsMap, nil
}

// GetProfile retrieves a profile by npub, fetching from nostr if not cached
func (cfg *Config) GetProfile(npub string) (*Profile, error) {
	// Decode npub to get public key
	var pub string
	if prefix, decoded, err := nip19.Decode(npub); err == nil {
		if prefix == "npub" {
			pub = decoded.(string)
		} else {
			return nil, fmt.Errorf("invalid npub format: %s", npub)
		}
	} else {
		// Maybe it's already a hex public key
		pub = npub
	}

	// Check cache first and see if it's fresh (less than 24 hours old)
	if profile, ok := cfg.profiles[pub]; ok {
		if !profile.FetchedAt.IsZero() && time.Since(profile.FetchedAt) < 24*time.Hour {
			return &profile, nil
		}
		// Profile exists but is stale, will refetch
	}

	// Fetch from nostr using pool with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	relays := []string{}
	for k, v := range cfg.Relays {
		if v.Read {
			relays = append(relays, k)
		}
	}

	if len(relays) == 0 {
		return nil, errors.New("no read relays available")
	}

	// Query for kind 0 (profile metadata)
	events := cfg.pool.QuerySingle(ctx, relays, nostr.Filter{
		Kinds:   []int{nostr.KindProfileMetadata},
		Authors: []string{pub},
		Limit:   1,
	})

	if events == nil {
		// If fetch fails but we have a stale cache, return it
		if profile, ok := cfg.profiles[pub]; ok {
			return &profile, nil
		}
		return nil, fmt.Errorf("profile not found for %s", npub)
	}

	var profile Profile
	if err := json.Unmarshal([]byte(events.Content), &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile: %w", err)
	}

	// Set fetch timestamp and cache the profile
	profile.FetchedAt = time.Now()
	cfg.profiles[pub] = profile
	cfg.profileChanged = true

	return &profile, nil
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
	var profilesFp string
	if profile == "" {
		fp = filepath.Join(dir, "config.json")
		profilesFp = filepath.Join(dir, "profiles.json")
	} else {
		fp = filepath.Join(dir, "config-"+profile+".json")
		profilesFp = filepath.Join(dir, "profiles-"+profile+".json")
	}

	// Save config
	b, err := json.MarshalIndent(&cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(fp, b, 0644); err != nil {
		return err
	}

	// Save profiles only if changed
	if cfg.profileChanged && cfg.profiles != nil && len(cfg.profiles) > 0 {
		profilesData, err := json.MarshalIndent(cfg.profiles, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(profilesFp, profilesData, 0644); err != nil {
			return err
		}
	}

	return nil
}

func (cfg *Config) saveProfiles(profile string) error {
	if cfg.tempRelay || !cfg.profileChanged {
		return nil
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "algia")

	var profilesFp string
	if profile == "" {
		profilesFp = filepath.Join(dir, "profiles.json")
	} else {
		profilesFp = filepath.Join(dir, "profiles-"+profile+".json")
	}

	// Save profiles only if changed
	if cfg.profiles != nil && len(cfg.profiles) > 0 {
		profilesData, err := json.MarshalIndent(cfg.profiles, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(profilesFp, profilesData, 0644); err != nil {
			return err
		}
	}

	return nil
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
				profile, err := cfg.GetProfile(ev.PubKey)
				if err == nil {
					events = append(events, Event{
						Event:   ev,
						Profile: *profile,
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
		profile, err := cfg.GetProfile(ev.PubKey)
		if err == nil {
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

// PrintEvent prints a single event
func (cfg *Config) PrintEvent(ev *nostr.Event, j, extra bool) {
	if j {
		if extra {
			// Check cache only, don't fetch
			if profile, ok := cfg.profiles[ev.PubKey]; ok {
				json.NewEncoder(os.Stdout).Encode(Event{
					Event:   ev,
					Profile: profile,
				})
			} else {
				json.NewEncoder(os.Stdout).Encode(ev)
			}
		} else {
			json.NewEncoder(os.Stdout).Encode(ev)
		}
		return
	}

	// Check cache only, don't fetch
	if profile, ok := cfg.profiles[ev.PubKey]; ok {
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

// Events is
func (cfg *Config) Events(filter nostr.Filter) []*nostr.Event {
	rf := Relay{Read: true}
	if filter.Search != "" {
		rf.Search = true
	}
	var mu sync.Mutex
	found := false
	var m sync.Map
	cfg.Do(rf, func(ctx context.Context, relay *nostr.Relay) bool {
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

// StreamEvents streams events as they arrive, calling the callback for each new event
// If closeOnEOSE is true, it stops after receiving EOSE from all relays
func (cfg *Config) StreamEvents(filter nostr.Filter, closeOnEOSE bool, callback func(*nostr.Event)) {
	ctx := context.Background()

	// Get read relays
	relays := []string{}
	for k, v := range cfg.Relays {
		if v.Read {
			// Skip search relays unless filter has search
			if filter.Search != "" && !v.Search {
				continue
			}
			relays = append(relays, k)
		}
	}

	if len(relays) == 0 {
		return
	}

	// Choose SubMany or SubManyEose based on closeOnEOSE flag
	var eventChan chan nostr.RelayEvent
	if closeOnEOSE {
		eventChan = cfg.pool.SubManyEose(ctx, relays, nostr.Filters{filter})
	} else {
		eventChan = cfg.pool.SubMany(ctx, relays, nostr.Filters{filter})
	}

	for ie := range eventChan {
		ev := ie.Event
		if ev == nil {
			continue
		}

		if ev.Kind == nostr.KindEncryptedDirectMessage || ev.Kind == nostr.KindCategorizedBookmarksList {
			if err := cfg.Decode(ev); err != nil {
				continue
			}
		}
		callback(ev)
	}
}

func doVersion(cCtx *cli.Context) error {
	fmt.Println(version)
	return nil
}

func clientTag(ev *nostr.Event) {
	ev.Tags = ev.Tags.AppendUnique(nostr.Tag{"client", "algia", "31990:2c7cc62a697ea3a7826521f3fd34f0cb273693cbe5e9310f35449f43622a5cdc:1727520612646", "wss://nostr.compile-error.net"})
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
					&cli.StringSliceFlag{Name: "tag"},
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
				Name:      "mcp",
				Usage:     "mcp server",
				UsageText: "algia mcp",
				HelpName:  "mcp",
				Action:    doMcp,
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
				Name: "event",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "stdin"},
					&cli.IntFlag{Name: "kind", Required: true},
					&cli.StringFlag{Name: "content"},
					&cli.StringSliceFlag{Name: "tag"},
				},
				Usage:     "send event",
				UsageText: "algia event ...",
				HelpName:  "event",
				Action:    doEvent,
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
				"config":  cfg,
				"profile": profile,
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
		After: func(cCtx *cli.Context) error {
			if cCtx.Args().Get(0) == "version" {
				return nil
			}
			if cfg, ok := cCtx.App.Metadata["config"].(*Config); ok {
				if profile, ok := cCtx.App.Metadata["profile"].(string); ok {
					return cfg.saveProfiles(profile)
				}
			}
			return nil
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

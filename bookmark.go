package main

import (
	"context"
	"errors"

	"github.com/urfave/cli/v2"

	"github.com/nbd-wtf/go-nostr"
)

func doBMList(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	eevs, err := callBookmarks(&bookmarksArg{
		ctx: context.Background(),
		cfg: cfg,
		n:   cCtx.Int("n"),
	})
	if err != nil {
		return err
	}
	cfg.PrintEvents(eevs, nil, cCtx.Bool("json"), cCtx.Bool("extra"))
	return nil
}

type bookmarksArg struct {
	ctx context.Context
	cfg *Config
	n   int
}

func callBookmarks(arg *bookmarksArg) ([]*nostr.Event, error) {
	_, pub, err := getSkAndPub(arg.cfg)
	if err != nil {
		return nil, err
	}
	filter := nostr.Filter{
		Kinds:   []int{nostr.KindCategorizedBookmarksList},
		Authors: []string{pub},
		Tags:    nostr.TagMap{"d": []string{"bookmark"}},
		Limit:   arg.n,
	}
	evs, err := arg.cfg.QueryEvents(arg.ctx, nostr.Filters{filter})
	if err != nil {
		return nil, err
	}
	be := []string{}
	for _, ev := range evs {
		for _, tag := range ev.Tags {
			if len(tag) > 1 && tag[0] == "e" {
				be = append(be, tag[1:]...)
			}
		}
	}
	if len(be) == 0 {
		return nil, nil
	}
	return arg.cfg.QueryEvents(arg.ctx, nostr.Filters{{
		Kinds: []int{nostr.KindTextNote},
		IDs:   be,
	}})
}

func doBMPost(cCtx *cli.Context) error {
	return errors.New("Not Implemented")
}

// bmCommand returns the "bm" parent command with its subcommands.
func bmCommand() *cli.Command {
	return &cli.Command{
		Name:  "bm",
		Usage: "bookmarks (NIP-51 kind 10003)",
		Subcommands: []*cli.Command{
			{
				Name: "list",
				Flags: []cli.Flag{
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
				},
				Usage:     "show bookmarks",
				UsageText: "algia bm list",
				Action:    doBMList,
			},
			{
				Name:      "post",
				Usage:     "post bookmark",
				UsageText: "algia bm post [note]",
				ArgsUsage: "[note]",
				Action:    doBMPost,
			},
		},
	}
}

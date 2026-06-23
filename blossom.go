package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/nbd-wtf/go-nostr/keyer"
	"github.com/nbd-wtf/go-nostr/nipb0/blossom"
	"github.com/urfave/cli/v2"
)

// blossomServers resolves the target Blossom media servers. The --server/-s
// flag takes precedence over the blossom-servers field in the config.
func blossomServers(cCtx *cli.Context, cfg *Config) ([]string, error) {
	servers := cCtx.StringSlice("server")
	if len(servers) == 0 {
		servers = cfg.BlossomServers
	}
	if len(servers) == 0 {
		return nil, errors.New("no blossom server configured; set \"blossom-servers\" in config or pass --server")
	}
	return servers, nil
}

// blossomClient builds a Blossom client for the given server signed with the
// configured private key.
func blossomClient(cfg *Config, server string) (*blossom.Client, error) {
	sk, _, err := getSkAndPub(cfg)
	if err != nil {
		return nil, err
	}
	signer, err := keyer.NewPlainKeySigner(sk)
	if err != nil {
		return nil, err
	}
	return blossom.NewClient(server, signer), nil
}

func doBlossomUpload(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := blossomServers(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var uploaded int
	for _, path := range cCtx.Args().Slice() {
		for _, server := range servers {
			client, err := blossomClient(cfg, server)
			if err != nil {
				return err
			}
			bd, err := client.UploadFile(ctx, path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", server, path, err)
				continue
			}
			uploaded++
			if cCtx.Bool("json") {
				fmt.Println(bd.String())
			} else {
				fmt.Println(bd.URL)
			}
		}
	}
	if uploaded == 0 {
		return errors.New("cannot upload")
	}
	return nil
}

func doBlossomList(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := blossomServers(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	for _, server := range servers {
		client, err := blossomClient(cfg, server)
		if err != nil {
			return err
		}
		bds, err := client.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", server, err)
			continue
		}
		if cCtx.Bool("json") {
			b, err := json.Marshal(bds)
			if err != nil {
				return err
			}
			fmt.Println(string(b))
			continue
		}
		for _, bd := range bds {
			fmt.Printf("%s\t%d\t%s\t%s\n", bd.SHA256, bd.Size, bd.Type, bd.URL)
		}
	}
	return nil
}

func doBlossomGet(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := blossomServers(cCtx, cfg)
	if err != nil {
		return err
	}
	hash := cCtx.Args().First()
	out := cCtx.String("o")

	ctx := context.Background()
	var lastErr error
	for _, server := range servers {
		client, err := blossomClient(cfg, server)
		if err != nil {
			return err
		}
		if out != "" {
			if err := client.DownloadToFile(ctx, hash, out); err != nil {
				lastErr = err
				continue
			}
			return nil
		}
		b, err := client.Download(ctx, hash)
		if err != nil {
			lastErr = err
			continue
		}
		_, err = os.Stdout.Write(b)
		return err
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("%s not found", hash)
}

func doBlossomDelete(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := blossomServers(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var deleted int
	for _, hash := range cCtx.Args().Slice() {
		for _, server := range servers {
			client, err := blossomClient(cfg, server)
			if err != nil {
				return err
			}
			if err := client.Delete(ctx, hash); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", server, hash, err)
				continue
			}
			deleted++
		}
	}
	if deleted == 0 {
		return errors.New("cannot delete")
	}
	return nil
}

func doBlossomCheck(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := blossomServers(cCtx, cfg)
	if err != nil {
		return err
	}
	hash := cCtx.Args().First()

	ctx := context.Background()
	for _, server := range servers {
		client, err := blossomClient(cfg, server)
		if err != nil {
			return err
		}
		if err := client.Check(ctx, hash); err != nil {
			fmt.Printf("%s\tNG\t%v\n", server, err)
		} else {
			fmt.Printf("%s\tOK\n", server)
		}
	}
	return nil
}

// blossomCommand returns the "blossom" parent command with its subcommands.
func blossomCommand() *cli.Command {
	serverFlag := &cli.StringSliceFlag{Name: "server", Aliases: []string{"s"}, Usage: "blossom server URL (overrides config; repeatable)"}
	return &cli.Command{
		Name:  "blossom",
		Usage: "operate on Blossom media servers (BUD-01/02)",
		Subcommands: []*cli.Command{
			{
				Name: "upload",
				Flags: []cli.Flag{
					serverFlag,
					&cli.BoolFlag{Name: "json", Usage: "output JSON blob descriptor"},
				},
				Usage:     "upload file(s) to the blossom server(s)",
				UsageText: "algia blossom upload [-s <server>...] <file> [file...]",
				ArgsUsage: "<file> [file...]",
				Action:    doBlossomUpload,
			},
			{
				Name: "list",
				Flags: []cli.Flag{
					serverFlag,
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
				},
				Usage:     "list your blobs on the blossom server(s)",
				UsageText: "algia blossom list [-s <server>...]",
				Action:    doBlossomList,
			},
			{
				Name: "get",
				Flags: []cli.Flag{
					serverFlag,
					&cli.StringFlag{Name: "o", Usage: "output file (default: stdout)"},
				},
				Usage:     "download a blob by its sha256 hash",
				UsageText: "algia blossom get [-s <server>...] [-o <file>] <sha256>",
				ArgsUsage: "<sha256>",
				Action:    doBlossomGet,
			},
			{
				Name: "delete",
				Flags: []cli.Flag{
					serverFlag,
				},
				Usage:     "delete blob(s) by sha256 hash from the blossom server(s)",
				UsageText: "algia blossom delete [-s <server>...] <sha256> [sha256...]",
				ArgsUsage: "<sha256> [sha256...]",
				Action:    doBlossomDelete,
			},
			{
				Name: "check",
				Flags: []cli.Flag{
					serverFlag,
				},
				Usage:     "check whether a blob exists on the blossom server(s)",
				UsageText: "algia blossom check [-s <server>...] <sha256>",
				ArgsUsage: "<sha256>",
				Action:    doBlossomCheck,
			},
		},
	}
}

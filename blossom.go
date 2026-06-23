package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/nbd-wtf/go-nostr"
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

// normalizeServer ensures the server has an http(s) scheme and no trailing slash.
func normalizeServer(s string) string {
	if !strings.HasPrefix(s, "http") {
		s = "https://" + s
	}
	return strings.TrimSuffix(s, "/")
}

// hashFromURL extracts the sha256 of a blob from its URL (the last path
// segment, without any extension).
func hashFromURL(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	base := u[strings.LastIndex(u, "/")+1:]
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	return base
}

// blossomAuthHeader builds a BUD-01 "Nostr <base64(event)>" Authorization header
// (kind 24242) signed with sk, with the given action verb and target hash.
func blossomAuthHeader(sk, verb, hash string) (string, error) {
	ev := nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      24242,
		Content:   "blossom stuff",
		Tags: nostr.Tags{
			nostr.Tag{"t", verb},
			nostr.Tag{"x", hash},
			nostr.Tag{"expiration", strconv.FormatInt(int64(nostr.Now())+300, 10)},
		},
	}
	if err := ev.Sign(sk); err != nil {
		return "", err
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(b), nil
}

// mirrorBlob asks the destination server to mirror the blob at sourceURL
// (BUD-04 PUT /mirror) and returns the resulting blob descriptor.
func mirrorBlob(ctx context.Context, sk, server, sourceURL, hash string) (*blossom.BlobDescriptor, error) {
	body, err := json.Marshal(map[string]string{"url": sourceURL})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "PUT", normalizeServer(server)+"/mirror", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	auth, err := blossomAuthHeader(sk, "upload", hash)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", auth)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(resp.Header.Get("X-Reason"))
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, msg)
	}
	var bd blossom.BlobDescriptor
	if err := json.Unmarshal(b, &bd); err != nil {
		return nil, err
	}
	return &bd, nil
}

// srcBlob is a blob enumerated on a source server: its public URL and sha256.
type srcBlob struct {
	URL  string
	Hash string
}

// tagValue returns the value of the first tag with the given key, or "".
func tagValue(tags nostr.Tags, key string) string {
	if t := tags.GetFirst([]string{key}); t != nil {
		return t.Value()
	}
	return ""
}

// blossomListAll lists every blob owned by the user on a Blossom source server.
func blossomListAll(ctx context.Context, cfg *Config, from string) ([]srcBlob, error) {
	client, err := blossomClient(cfg, from)
	if err != nil {
		return nil, err
	}
	bds, err := client.List(ctx)
	if err != nil {
		return nil, err
	}
	blobs := make([]srcBlob, 0, len(bds))
	for _, b := range bds {
		u := b.URL
		if u == "" {
			u = normalizeServer(from) + "/" + b.SHA256
		}
		blobs = append(blobs, srcBlob{URL: u, Hash: b.SHA256})
	}
	return blobs, nil
}

// nip98Header builds a NIP-98 "Nostr <base64(event)>" Authorization header
// (kind 27235) for the given url and HTTP method, signed with sk.
func nip98Header(sk, url, method string) (string, error) {
	ev := nostr.Event{
		Kind:      27235,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			nostr.Tag{"u", url},
			nostr.Tag{"method", method},
		},
	}
	if err := ev.Sign(sk); err != nil {
		return "", err
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(b), nil
}

// nip96APIURL fetches the NIP-96 server config and returns its api_url.
func nip96APIURL(ctx context.Context, server string) (string, error) {
	url := normalizeServer(server) + "/.well-known/nostr/nip96.json"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("well-known status %d", resp.StatusCode)
	}
	var cfg struct {
		APIURL string `json:"api_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return "", err
	}
	if cfg.APIURL == "" {
		return "", errors.New("no api_url in nip96.json")
	}
	return strings.TrimSuffix(cfg.APIURL, "/"), nil
}

// nip96ListAll enumerates every file the user owns on a NIP-96 source server by
// paging through its listing endpoint (NIP-98 authenticated).
func nip96ListAll(ctx context.Context, sk, from string) ([]srcBlob, error) {
	apiURL, err := nip96APIURL(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("nip96 config: %w", err)
	}

	var blobs []srcBlob
	const count = 100
	for page := 0; ; page++ {
		pageURL := fmt.Sprintf("%s?page=%d&count=%d", apiURL, page, count)
		req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
		if err != nil {
			return nil, err
		}
		auth, err := nip98Header(sk, pageURL, "GET")
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", auth)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("list status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var lr struct {
			Total int `json:"total"`
			Files []struct {
				Tags nostr.Tags `json:"tags"`
			} `json:"files"`
		}
		if err := json.Unmarshal(body, &lr); err != nil {
			return nil, err
		}
		if len(lr.Files) == 0 {
			break
		}
		for _, f := range lr.Files {
			url := tagValue(f.Tags, "url")
			hash := tagValue(f.Tags, "x")
			if hash == "" {
				hash = tagValue(f.Tags, "ox")
			}
			if url == "" || hash == "" {
				continue
			}
			blobs = append(blobs, srcBlob{URL: url, Hash: hash})
		}
		if lr.Total > 0 && len(blobs) >= lr.Total {
			break
		}
	}
	return blobs, nil
}

func doBlossomMirror(cCtx *cli.Context) error {
	all := cCtx.Bool("all")
	if cCtx.Args().Len() == 0 && !all {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := blossomServers(cCtx, cfg)
	if err != nil {
		return err
	}
	sk, _, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}
	from := cCtx.String("from")

	// --all: enumerate every blob of yours on the source server and mirror each
	// one into the destination server(s). The source may be Blossom (default)
	// or NIP-96 (--nip96).
	if all {
		if from == "" {
			return errors.New("--all needs --from <source server>")
		}
		ctx := context.Background()
		var blobs []srcBlob
		if cCtx.Bool("nip96") {
			blobs, err = nip96ListAll(ctx, sk, from)
		} else {
			blobs, err = blossomListAll(ctx, cfg, from)
		}
		if err != nil {
			return fmt.Errorf("failed to list blobs on %s: %w", from, err)
		}
		if len(blobs) == 0 {
			fmt.Fprintf(os.Stderr, "no blobs found on %s\n", from)
			return nil
		}
		var mirrored, failed int
		for _, b := range blobs {
			for _, server := range servers {
				bd, err := mirrorBlob(ctx, sk, server, b.URL, b.Hash)
				if err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "%s: %s: %v\n", server, b.Hash, err)
					continue
				}
				mirrored++
				if cCtx.Bool("json") {
					fmt.Println(bd.String())
				} else {
					fmt.Println(bd.URL)
				}
			}
		}
		fmt.Fprintf(os.Stderr, "mirrored %d, failed %d (of %d blobs)\n", mirrored, failed, len(blobs))
		if mirrored == 0 {
			return errors.New("cannot mirror")
		}
		return nil
	}

	src := cCtx.Args().First()
	var sourceURL, hash string
	if nostr.IsValid32ByteHex(src) {
		if from == "" {
			return errors.New("a bare hash needs --from <source server>")
		}
		hash = src
		sourceURL = normalizeServer(from) + "/" + hash
	} else {
		sourceURL = src
		hash = hashFromURL(src)
		if !nostr.IsValid32ByteHex(hash) {
			return fmt.Errorf("cannot derive sha256 from %q", src)
		}
	}

	ctx := context.Background()
	var mirrored int
	for _, server := range servers {
		bd, err := mirrorBlob(ctx, sk, server, sourceURL, hash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", server, err)
			continue
		}
		mirrored++
		if cCtx.Bool("json") {
			fmt.Println(bd.String())
		} else {
			fmt.Println(bd.URL)
		}
	}
	if mirrored == 0 {
		return errors.New("cannot mirror")
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
			{
				Name: "mirror",
				Flags: []cli.Flag{
					serverFlag,
					&cli.StringFlag{Name: "from", Usage: "source server (required with --all or a bare hash)"},
					&cli.BoolFlag{Name: "all", Usage: "mirror all your blobs from --from into the dest server(s)"},
					&cli.BoolFlag{Name: "nip96", Usage: "treat --from as a NIP-96 server when listing with --all"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON blob descriptor"},
				},
				Usage:     "mirror blob(s) into the blossom server(s) (BUD-04)",
				UsageText: "algia blossom mirror [-s <dest>...] <source-url> | --from <src> <sha256> | --all --from <src>",
				ArgsUsage: "[source-url|sha256]",
				Action:    doBlossomMirror,
			},
		},
	}
}

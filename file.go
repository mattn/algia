package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/keyer"
	"github.com/nbd-wtf/go-nostr/nipb0/blossom"
	"github.com/urfave/cli/v2"
)

// Media server protocol types.
const (
	typeBlossom = "blossom"
	typeNIP96   = "nip96"
)

// fileServer is a configured media server together with the protocol used to
// talk to it. In config it may be written either as a bare URL string (treated
// as Blossom) or as an object {"url": "...", "type": "blossom"|"nip96"}. A URL
// may also carry a "nip96+"/"blossom+" prefix to select the type inline.
type fileServer struct {
	URL  string `json:"url"`
	Type string `json:"type"`
}

// parseFileServer turns a bare server string into a fileServer, honoring an
// optional "nip96+"/"blossom+" scheme prefix and defaulting to Blossom.
func parseFileServer(s string) fileServer {
	switch {
	case strings.HasPrefix(s, "nip96+"):
		return fileServer{URL: strings.TrimPrefix(s, "nip96+"), Type: typeNIP96}
	case strings.HasPrefix(s, "blossom+"):
		return fileServer{URL: strings.TrimPrefix(s, "blossom+"), Type: typeBlossom}
	default:
		return fileServer{URL: s, Type: typeBlossom}
	}
}

// UnmarshalJSON accepts either a bare string (backward compatible with the old
// blossom-servers config) or a {url,type} object.
func (fs *fileServer) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*fs = parseFileServer(s)
		return nil
	}
	type alias fileServer
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	if a.Type == "" {
		a.Type = typeBlossom
	}
	*fs = fileServer(a)
	return nil
}

// mediaClient is the protocol-agnostic interface used by the file subcommands.
// Both Blossom and NIP-96 backends implement it. Blob metadata is reported with
// blossom.BlobDescriptor regardless of backend.
type mediaClient interface {
	Upload(ctx context.Context, path string) (*blossom.BlobDescriptor, error)
	List(ctx context.Context) ([]blossom.BlobDescriptor, error)
	Download(ctx context.Context, hash string) ([]byte, error)
	DownloadToFile(ctx context.Context, hash, out string) error
	Delete(ctx context.Context, hash string) error
	Check(ctx context.Context, hash string) error
}

// fileServers resolves the target media servers. The --server/-s flag takes
// precedence over the file-servers field in the config. Flag values may use a
// "nip96+"/"blossom+" prefix to pick the protocol.
func fileServers(cCtx *cli.Context, cfg *Config) ([]fileServer, error) {
	if flags := cCtx.StringSlice("server"); len(flags) > 0 {
		servers := make([]fileServer, len(flags))
		for i, s := range flags {
			servers[i] = parseFileServer(s)
		}
		return servers, nil
	}
	if len(cfg.FileServers) == 0 {
		return nil, errors.New("no media server configured; set \"file-servers\" in config or pass --server")
	}
	return cfg.FileServers, nil
}

// newMediaClient builds the right backend for the given server.
func newMediaClient(cfg *Config, fs fileServer) (mediaClient, error) {
	sk, _, err := getSkAndPub(cfg)
	if err != nil {
		return nil, err
	}
	if fs.Type == typeNIP96 {
		return &nip96Client{sk: sk, server: fs.URL}, nil
	}
	signer, err := keyer.NewPlainKeySigner(sk)
	if err != nil {
		return nil, err
	}
	return &blossomMediaClient{c: blossom.NewClient(fs.URL, signer)}, nil
}

// blossomMediaClient adapts blossom.Client to the mediaClient interface.
type blossomMediaClient struct {
	c *blossom.Client
}

func (b *blossomMediaClient) Upload(ctx context.Context, path string) (*blossom.BlobDescriptor, error) {
	return b.c.UploadFile(ctx, path)
}
func (b *blossomMediaClient) List(ctx context.Context) ([]blossom.BlobDescriptor, error) {
	return b.c.List(ctx)
}
func (b *blossomMediaClient) Download(ctx context.Context, hash string) ([]byte, error) {
	return b.c.Download(ctx, hash)
}
func (b *blossomMediaClient) DownloadToFile(ctx context.Context, hash, out string) error {
	return b.c.DownloadToFile(ctx, hash, out)
}
func (b *blossomMediaClient) Delete(ctx context.Context, hash string) error {
	return b.c.Delete(ctx, hash)
}
func (b *blossomMediaClient) Check(ctx context.Context, hash string) error {
	return b.c.Check(ctx, hash)
}

// nip96Client implements mediaClient against a NIP-96 HTTP file storage server,
// authenticating each request with NIP-98.
type nip96Client struct {
	sk     string
	server string
	apiURL string // cached result of nip96APIURL
}

// api returns the (cached) api_url advertised in the server's nip96.json.
func (c *nip96Client) api(ctx context.Context) (string, error) {
	if c.apiURL == "" {
		u, err := nip96APIURL(ctx, c.server)
		if err != nil {
			return "", err
		}
		c.apiURL = u
	}
	return c.apiURL, nil
}

func (c *nip96Client) Upload(ctx context.Context, path string) (*blossom.BlobDescriptor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ct := mime.TypeByExtension(filepath.Ext(path))
	return c.uploadData(ctx, filepath.Base(path), ct, data)
}

// uploadData posts a multipart file body to the NIP-96 api_url and maps the
// returned nip94 event into a BlobDescriptor.
func (c *nip96Client) uploadData(ctx context.Context, filename, contentType string, data []byte) (*blossom.BlobDescriptor, error) {
	apiURL, err := c.api(ctx)
	if err != nil {
		return nil, fmt.Errorf("nip96 config: %w", err)
	}

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	body := buf.Bytes()
	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	auth, err := nip98Header(c.sk, apiURL, "POST", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", auth)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("upload status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var ur struct {
		Status     string `json:"status"`
		Message    string `json:"message"`
		Nip94Event struct {
			Tags nostr.Tags `json:"tags"`
		} `json:"nip94_event"`
	}
	if err := json.Unmarshal(b, &ur); err != nil {
		return nil, err
	}
	if ur.Status != "" && ur.Status != "success" {
		return nil, fmt.Errorf("upload failed: %s", ur.Message)
	}
	bd := blobFromTags(ur.Nip94Event.Tags)
	if bd.URL == "" {
		return nil, fmt.Errorf("no url in upload response: %s", strings.TrimSpace(string(b)))
	}
	return &bd, nil
}

func (c *nip96Client) List(ctx context.Context) ([]blossom.BlobDescriptor, error) {
	apiURL, err := c.api(ctx)
	if err != nil {
		return nil, fmt.Errorf("nip96 config: %w", err)
	}

	var bds []blossom.BlobDescriptor
	const count = 100
	for page := 0; ; page++ {
		pageURL := fmt.Sprintf("%s?page=%d&count=%d", apiURL, page, count)
		req, err := http.NewRequestWithContext(ctx, "GET", pageURL, nil)
		if err != nil {
			return nil, err
		}
		auth, err := nip98Header(c.sk, pageURL, "GET", nil)
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
			bd := blobFromTags(f.Tags)
			if bd.URL == "" && bd.SHA256 == "" {
				continue
			}
			bds = append(bds, bd)
		}
		if lr.Total > 0 && len(bds) >= lr.Total {
			break
		}
	}
	return bds, nil
}

func (c *nip96Client) Download(ctx context.Context, hash string) ([]byte, error) {
	url, err := c.urlForHash(ctx, hash)
	if err != nil {
		return nil, err
	}
	b, _, err := httpGetBytes(ctx, url)
	return b, err
}

func (c *nip96Client) DownloadToFile(ctx context.Context, hash, out string) error {
	b, err := c.Download(ctx, hash)
	if err != nil {
		return err
	}
	return os.WriteFile(out, b, 0644)
}

func (c *nip96Client) Delete(ctx context.Context, hash string) error {
	apiURL, err := c.api(ctx)
	if err != nil {
		return fmt.Errorf("nip96 config: %w", err)
	}
	url := apiURL + "/" + hash
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	auth, err := nip98Header(c.sk, url, "DELETE", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("delete status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *nip96Client) Check(ctx context.Context, hash string) error {
	bds, err := c.List(ctx)
	if err != nil {
		return err
	}
	for _, bd := range bds {
		if bd.SHA256 == hash {
			return nil
		}
	}
	return fmt.Errorf("%s not found", hash)
}

// urlForHash finds the public URL of a blob by listing the user's files and
// matching the sha256, falling back to <server>/<hash>.
func (c *nip96Client) urlForHash(ctx context.Context, hash string) (string, error) {
	bds, err := c.List(ctx)
	if err != nil {
		return "", err
	}
	for _, bd := range bds {
		if bd.SHA256 == hash && bd.URL != "" {
			return bd.URL, nil
		}
	}
	return normalizeServer(c.server) + "/" + hash, nil
}

// blobFromTags builds a BlobDescriptor from NIP-94 file tags.
func blobFromTags(tags nostr.Tags) blossom.BlobDescriptor {
	bd := blossom.BlobDescriptor{
		URL:    tagValue(tags, "url"),
		SHA256: tagValue(tags, "x"),
		Type:   tagValue(tags, "m"),
	}
	if bd.SHA256 == "" {
		bd.SHA256 = tagValue(tags, "ox")
	}
	if s := tagValue(tags, "size"); s != "" {
		bd.Size, _ = strconv.Atoi(s)
	}
	return bd
}

// httpGetBytes performs a plain GET and returns the body and its content type.
func httpGetBytes(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("status %d", resp.StatusCode)
	}
	return b, resp.Header.Get("Content-Type"), nil
}

func doFileUpload(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := fileServers(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var uploaded int
	for _, path := range cCtx.Args().Slice() {
		for _, server := range servers {
			client, err := newMediaClient(cfg, server)
			if err != nil {
				return err
			}
			bd, err := client.Upload(ctx, path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", server.URL, path, err)
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

func doFileList(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := fileServers(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	for _, server := range servers {
		client, err := newMediaClient(cfg, server)
		if err != nil {
			return err
		}
		bds, err := client.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", server.URL, err)
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

func doFileGet(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := fileServers(cCtx, cfg)
	if err != nil {
		return err
	}
	hash := cCtx.Args().First()
	out := cCtx.String("o")

	ctx := context.Background()
	var lastErr error
	for _, server := range servers {
		client, err := newMediaClient(cfg, server)
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

func doFileDelete(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := fileServers(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var deleted int
	for _, hash := range cCtx.Args().Slice() {
		for _, server := range servers {
			client, err := newMediaClient(cfg, server)
			if err != nil {
				return err
			}
			if err := client.Delete(ctx, hash); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", server.URL, hash, err)
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

func doFileCheck(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := fileServers(cCtx, cfg)
	if err != nil {
		return err
	}
	hash := cCtx.Args().First()

	ctx := context.Background()
	for _, server := range servers {
		client, err := newMediaClient(cfg, server)
		if err != nil {
			return err
		}
		if err := client.Check(ctx, hash); err != nil {
			fmt.Printf("%s\tNG\t%v\n", server.URL, err)
		} else {
			fmt.Printf("%s\tOK\n", server.URL)
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

// mirrorTo replicates the blob at sourceURL into the destination server. Blossom
// destinations use the BUD-04 /mirror endpoint; NIP-96 destinations have no
// server-side mirror, so the blob is fetched and re-uploaded.
func mirrorTo(ctx context.Context, dest fileServer, sk, sourceURL, hash string) (*blossom.BlobDescriptor, error) {
	if dest.Type == typeNIP96 {
		data, ct, err := httpGetBytes(ctx, sourceURL)
		if err != nil {
			return nil, err
		}
		name := hash
		if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
			name += exts[0]
		}
		client := &nip96Client{sk: sk, server: dest.URL}
		return client.uploadData(ctx, name, ct, data)
	}
	return mirrorBlob(ctx, sk, dest.URL, sourceURL, hash)
}

// tagValue returns the value of the first tag with the given key, or "".
func tagValue(tags nostr.Tags, key string) string {
	if t := tags.GetFirst([]string{key}); t != nil {
		return t.Value()
	}
	return ""
}

// nip98Header builds a NIP-98 "Nostr <base64(event)>" Authorization header
// (kind 27235) for the given url and HTTP method, signed with sk. When body is
// non-empty its sha256 is included as a "payload" tag.
func nip98Header(sk, url, method string, body []byte) (string, error) {
	ev := nostr.Event{
		Kind:      27235,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			nostr.Tag{"u", url},
			nostr.Tag{"method", method},
		},
	}
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		ev.Tags = append(ev.Tags, nostr.Tag{"payload", hex.EncodeToString(sum[:])})
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

func doFileMirror(cCtx *cli.Context) error {
	all := cCtx.Bool("all")
	if cCtx.Args().Len() == 0 && !all {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	servers, err := fileServers(cCtx, cfg)
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
	// or NIP-96 (--nip96, or a nip96+ prefix on --from).
	if all {
		if from == "" {
			return errors.New("--all needs --from <source server>")
		}
		ctx := context.Background()
		source := parseFileServer(from)
		if cCtx.Bool("nip96") {
			source.Type = typeNIP96
		}
		srcClient, err := newMediaClient(cfg, source)
		if err != nil {
			return err
		}
		bds, err := srcClient.List(ctx)
		if err != nil {
			return fmt.Errorf("failed to list blobs on %s: %w", source.URL, err)
		}
		if len(bds) == 0 {
			fmt.Fprintf(os.Stderr, "no blobs found on %s\n", source.URL)
			return nil
		}
		var mirrored, failed int
		for _, blob := range bds {
			sourceURL := blob.URL
			if sourceURL == "" {
				sourceURL = normalizeServer(source.URL) + "/" + blob.SHA256
			}
			for _, server := range servers {
				bd, err := mirrorTo(ctx, server, sk, sourceURL, blob.SHA256)
				if err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "%s: %s: %v\n", server.URL, blob.SHA256, err)
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
		fmt.Fprintf(os.Stderr, "mirrored %d, failed %d (of %d blobs)\n", mirrored, failed, len(bds))
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
		sourceURL = normalizeServer(parseFileServer(from).URL) + "/" + hash
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
		bd, err := mirrorTo(ctx, server, sk, sourceURL, hash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", server.URL, err)
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

// fileCommand returns the "file" parent command with its subcommands. It
// operates on Blossom and NIP-96 media servers configured in "file-servers".
func fileCommand() *cli.Command {
	serverFlag := &cli.StringSliceFlag{Name: "server", Aliases: []string{"s"}, Usage: "media server URL, optionally nip96+/blossom+ prefixed (overrides config; repeatable)"}
	return &cli.Command{
		Name:  "file",
		Usage: "operate on Blossom/NIP-96 media servers",
		Subcommands: []*cli.Command{
			{
				Name: "upload",
				Flags: []cli.Flag{
					serverFlag,
					&cli.BoolFlag{Name: "json", Usage: "output JSON blob descriptor"},
				},
				Usage:     "upload file(s) to the media server(s)",
				UsageText: "algia file upload [-s <server>...] <file> [file...]",
				ArgsUsage: "<file> [file...]",
				Action:    doFileUpload,
			},
			{
				Name: "list",
				Flags: []cli.Flag{
					serverFlag,
					&cli.BoolFlag{Name: "json", Usage: "output JSON"},
				},
				Usage:     "list your blobs on the media server(s)",
				UsageText: "algia file list [-s <server>...]",
				Action:    doFileList,
			},
			{
				Name: "get",
				Flags: []cli.Flag{
					serverFlag,
					&cli.StringFlag{Name: "o", Usage: "output file (default: stdout)"},
				},
				Usage:     "download a blob by its sha256 hash",
				UsageText: "algia file get [-s <server>...] [-o <file>] <sha256>",
				ArgsUsage: "<sha256>",
				Action:    doFileGet,
			},
			{
				Name: "delete",
				Flags: []cli.Flag{
					serverFlag,
				},
				Usage:     "delete blob(s) by sha256 hash from the media server(s)",
				UsageText: "algia file delete [-s <server>...] <sha256> [sha256...]",
				ArgsUsage: "<sha256> [sha256...]",
				Action:    doFileDelete,
			},
			{
				Name: "check",
				Flags: []cli.Flag{
					serverFlag,
				},
				Usage:     "check whether a blob exists on the media server(s)",
				UsageText: "algia file check [-s <server>...] <sha256>",
				ArgsUsage: "<sha256>",
				Action:    doFileCheck,
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
				Usage:     "mirror blob(s) into the media server(s)",
				UsageText: "algia file mirror [-s <dest>...] <source-url> | --from <src> <sha256> | --all --from <src>",
				ArgsUsage: "[source-url|sha256]",
				Action:    doFileMirror,
			},
		},
	}
}

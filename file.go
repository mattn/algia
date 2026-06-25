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

// serverClient pairs a configured server with its ready-to-use client.
type serverClient struct {
	server fileServer
	client mediaClient
}

// resolveClients resolves the target servers (-s flag or config) and builds a
// client for each, so subcommands can just range over the result.
func resolveClients(cCtx *cli.Context, cfg *Config) ([]serverClient, error) {
	servers, err := fileServers(cCtx, cfg)
	if err != nil {
		return nil, err
	}
	scs := make([]serverClient, 0, len(servers))
	for _, s := range servers {
		client, err := newMediaClient(cfg, s)
		if err != nil {
			return nil, err
		}
		scs = append(scs, serverClient{server: s, client: client})
	}
	return scs, nil
}

// printBlob writes a blob descriptor: JSON when --json is set, else its URL.
func printBlob(cCtx *cli.Context, bd *blossom.BlobDescriptor) {
	if cCtx.Bool("json") {
		fmt.Println(bd.String())
	} else {
		fmt.Println(bd.URL)
	}
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
	auth, err := nip98Header(c.sk, apiURL, "POST", body)
	if err != nil {
		return nil, err
	}
	b, _, err := httpDo(ctx, "POST", apiURL, body, map[string]string{
		"Content-Type":  w.FormDataContentType(),
		"Authorization": auth,
	})
	if err != nil {
		return nil, err
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
		auth, err := nip98Header(c.sk, pageURL, "GET", nil)
		if err != nil {
			return nil, err
		}
		body, _, err := httpDo(ctx, "GET", pageURL, nil, map[string]string{"Authorization": auth})
		if err != nil {
			return nil, err
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
	auth, err := nip98Header(c.sk, url, "DELETE", nil)
	if err != nil {
		return err
	}
	_, _, err = httpDo(ctx, "DELETE", url, nil, map[string]string{"Authorization": auth})
	return err
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

// statusError formats a non-2xx HTTP response, preferring the X-Reason header
// (sent by Blossom servers) and falling back to the response body.
func statusError(code int, header http.Header, body []byte) error {
	msg := strings.TrimSpace(header.Get("X-Reason"))
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	return fmt.Errorf("status %d: %s", code, msg)
}

// httpDo builds a request with the given headers, executes it, reads the body
// and turns a non-2xx status into a statusError. A nil body sends no payload.
func httpDo(ctx context.Context, method, url string, body []byte, headers map[string]string) ([]byte, http.Header, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, r)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, resp.Header, statusError(resp.StatusCode, resp.Header, b)
	}
	return b, resp.Header, nil
}

// httpHead issues a HEAD request and returns the (body-closed) response.
func httpHead(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return resp, nil
}

// httpGetBytes performs a plain GET and returns the body and its content type.
func httpGetBytes(ctx context.Context, url string) ([]byte, string, error) {
	b, header, err := httpDo(ctx, "GET", url, nil, nil)
	if err != nil {
		return nil, "", err
	}
	return b, header.Get("Content-Type"), nil
}

func doFileUpload(cCtx *cli.Context) error {
	if cCtx.Args().Len() == 0 {
		return cli.ShowSubcommandHelp(cCtx)
	}
	cfg := cCtx.App.Metadata["config"].(*Config)
	clients, err := resolveClients(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var uploaded int
	for _, path := range cCtx.Args().Slice() {
		for _, sc := range clients {
			bd, err := sc.client.Upload(ctx, path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", sc.server.URL, path, err)
				continue
			}
			uploaded++
			printBlob(cCtx, bd)
		}
	}
	if uploaded == 0 {
		return errors.New("cannot upload")
	}
	return nil
}

func doFileList(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	clients, err := resolveClients(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	for _, sc := range clients {
		bds, err := sc.client.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", sc.server.URL, err)
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
	clients, err := resolveClients(cCtx, cfg)
	if err != nil {
		return err
	}
	hash := cCtx.Args().First()
	out := cCtx.String("o")

	ctx := context.Background()
	var lastErr error
	for _, sc := range clients {
		if out != "" {
			if err := sc.client.DownloadToFile(ctx, hash, out); err != nil {
				lastErr = err
				continue
			}
			return nil
		}
		b, err := sc.client.Download(ctx, hash)
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
	clients, err := resolveClients(cCtx, cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var deleted int
	for _, hash := range cCtx.Args().Slice() {
		for _, sc := range clients {
			if err := sc.client.Delete(ctx, hash); err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", sc.server.URL, hash, err)
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
	clients, err := resolveClients(cCtx, cfg)
	if err != nil {
		return err
	}
	hash := cCtx.Args().First()

	ctx := context.Background()
	for _, sc := range clients {
		if err := sc.client.Check(ctx, hash); err != nil {
			fmt.Printf("%s\tNG\t%v\n", sc.server.URL, err)
		} else {
			fmt.Printf("%s\tOK\n", sc.server.URL)
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

// authHeader signs ev with sk and encodes it as the "Nostr <base64(event)>"
// Authorization value shared by BUD-01 (Blossom) and NIP-98 (HTTP) auth.
func authHeader(sk string, ev nostr.Event) (string, error) {
	if err := ev.Sign(sk); err != nil {
		return "", err
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(b), nil
}

// blossomAuthHeader builds a BUD-01 Authorization header (kind 24242) signed
// with sk, with the given action verb and target hash.
func blossomAuthHeader(sk, verb, hash string) (string, error) {
	return authHeader(sk, nostr.Event{
		CreatedAt: nostr.Now(),
		Kind:      24242,
		Content:   "blossom stuff",
		Tags: nostr.Tags{
			nostr.Tag{"t", verb},
			nostr.Tag{"x", hash},
			nostr.Tag{"expiration", strconv.FormatInt(int64(nostr.Now())+300, 10)},
		},
	})
}

// blossomPutBlob PUTs body to a Blossom endpoint with a BUD-01 upload
// authorization for hash and decodes the returned blob descriptor. It backs
// both the BUD-04 /mirror and BUD-02 /upload requests.
func blossomPutBlob(ctx context.Context, sk, url string, body []byte, contentType, hash string) (*blossom.BlobDescriptor, error) {
	auth, err := blossomAuthHeader(sk, "upload", hash)
	if err != nil {
		return nil, err
	}
	b, _, err := httpDo(ctx, "PUT", url, body, map[string]string{
		"Content-Type":  contentType,
		"Authorization": auth,
	})
	if err != nil {
		return nil, err
	}
	var bd blossom.BlobDescriptor
	if err := json.Unmarshal(b, &bd); err != nil {
		return nil, err
	}
	return &bd, nil
}

// mirrorBlob asks the destination server to mirror the blob at sourceURL
// (BUD-04 PUT /mirror) and returns the resulting blob descriptor.
func mirrorBlob(ctx context.Context, sk, server, sourceURL, hash string) (*blossom.BlobDescriptor, error) {
	body, err := json.Marshal(map[string]string{"url": sourceURL})
	if err != nil {
		return nil, err
	}
	return blossomPutBlob(ctx, sk, normalizeServer(server)+"/mirror", body, "application/json", hash)
}

// blossomUploadData uploads raw bytes to a Blossom server (BUD-02 PUT /upload),
// authorizing the given sha256 hash, and returns the resulting blob descriptor.
func blossomUploadData(ctx context.Context, sk, server string, data []byte, contentType, hash string) (*blossom.BlobDescriptor, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return blossomPutBlob(ctx, sk, normalizeServer(server)+"/upload", data, contentType, hash)
}

// fetchAndHash downloads the source URL and returns its bytes, content type and
// the sha256 of the actual bytes (which can differ from the hash in the URL when
// the source server is not content-addressed).
func fetchAndHash(ctx context.Context, sourceURL string) (data []byte, contentType, hash string, err error) {
	data, contentType, err = httpGetBytes(ctx, sourceURL)
	if err != nil {
		return nil, "", "", err
	}
	sum := sha256.Sum256(data)
	return data, contentType, hex.EncodeToString(sum[:]), nil
}

// mirrorTo replicates the blob at sourceURL into the destination server. Blossom
// destinations first try the efficient BUD-04 /mirror endpoint; if that fails
// (e.g. the source is not content-addressed, so the dest cannot verify the URL
// hash) the blob is downloaded and re-uploaded under its real sha256. NIP-96
// destinations have no server-side mirror, so they always download and re-upload.
//
// has reports whether the destination already holds a given sha256 (nil when not
// diffing). When the blob has to be downloaded to learn its real hash, that hash
// is checked with has: if present the upload is skipped and skipped=true is
// returned. This catches sources that advertise a different hash than the bytes
// they serve (so the cheap listing-hash diff cannot match).
func mirrorTo(ctx context.Context, dest fileServer, sk, sourceURL, hash string, has func(string) bool) (bd *blossom.BlobDescriptor, skipped bool, err error) {
	if dest.Type == typeNIP96 {
		data, ct, real, err := fetchAndHash(ctx, sourceURL)
		if err != nil {
			return nil, false, err
		}
		if has != nil && has(real) {
			return nil, true, nil
		}
		name := real
		if exts, _ := mime.ExtensionsByType(ct); len(exts) > 0 {
			name += exts[0]
		}
		client := &nip96Client{sk: sk, server: dest.URL}
		bd, err := client.uploadData(ctx, name, ct, data)
		return bd, false, err
	}
	bd, err = mirrorBlob(ctx, sk, dest.URL, sourceURL, hash)
	if err == nil {
		return bd, false, nil
	}
	// Fallback: download the blob ourselves, compute its real hash, and upload
	// the bytes directly (unless the destination already has that hash).
	data, ct, real, ferr := fetchAndHash(ctx, sourceURL)
	if ferr != nil {
		return nil, false, err
	}
	if has != nil && has(real) {
		return nil, true, nil
	}
	bd, err = blossomUploadData(ctx, sk, dest.URL, data, ct, real)
	return bd, false, err
}

// newDestChecker returns a function reporting whether a sha256 already exists on
// the destination. Blossom servers are probed per-hash with a HEAD request (their
// list can be capped server-side); NIP-96 servers are listed once (their listing
// is fully paginated) and the result cached in a set.
func newDestChecker(ctx context.Context, cfg *Config, server fileServer) (func(string) bool, error) {
	if server.Type == typeNIP96 {
		client, err := newMediaClient(cfg, server)
		if err != nil {
			return nil, err
		}
		bds, err := client.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: cannot list existing blobs: %v\n", server.URL, err)
			return func(string) bool { return false }, nil
		}
		set := make(map[string]bool, len(bds))
		for _, d := range bds {
			set[d.SHA256] = true
		}
		return func(h string) bool { return set[h] }, nil
	}
	return func(h string) bool { return blossomHas(ctx, server.URL, h) }, nil
}

// blossomHas reports whether server holds the blob with the given sha256, via a
// HEAD request (BUD-01 GET/HEAD /<sha256>).
func blossomHas(ctx context.Context, server, hash string) bool {
	resp, err := httpHead(ctx, normalizeServer(server)+"/"+hash)
	return err == nil && resp.StatusCode < 300
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
	return authHeader(sk, ev)
}

// nip96APIURL fetches the NIP-96 server config and returns its api_url.
func nip96APIURL(ctx context.Context, server string) (string, error) {
	url := normalizeServer(server) + "/.well-known/nostr/nip96.json"
	b, _, err := httpDo(ctx, "GET", url, nil, nil)
	if err != nil {
		return "", err
	}
	var cfg struct {
		APIURL string `json:"api_url"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return "", err
	}
	if cfg.APIURL == "" {
		return "", errors.New("no api_url in nip96.json")
	}
	return strings.TrimSuffix(cfg.APIURL, "/"), nil
}

// mirrorFromSource enumerates every blob the user owns on the source server and
// mirrors each into the destination server(s). When diff is true, blobs already
// present on a destination (matched by sha256) are skipped.
func mirrorFromSource(ctx context.Context, cCtx *cli.Context, cfg *Config, servers []fileServer, sk string, source fileServer, diff bool) error {
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

	// For a diff mirror, build a per-destination existence checker. Blossom
	// listings can be capped server-side (e.g. 100 entries), so checking each
	// hash with a HEAD request is more reliable than diffing against a list.
	checkers := map[string]func(string) bool{}
	if diff {
		for _, server := range servers {
			has, err := newDestChecker(ctx, cfg, server)
			if err != nil {
				return err
			}
			checkers[server.URL] = has
		}
	}

	dryRun := cCtx.Bool("dry-run")
	var mirrored, skipped, failed, planned int
	var plannedSize int64
	for _, blob := range bds {
		sourceURL := blob.URL
		if sourceURL == "" {
			sourceURL = normalizeServer(source.URL) + "/" + blob.SHA256
		}
		// The destination authorizes (and verifies) the sha256 of the bytes it
		// fetches from sourceURL, so derive the hash from the URL rather than
		// trusting the listing's sha256 (which may differ, e.g. when the server
		// reports the original hash but serves a transformed blob).
		hash := hashFromURL(sourceURL)
		if !nostr.IsValid32ByteHex(hash) {
			hash = blob.SHA256
		}
		for _, server := range servers {
			has := checkers[server.URL]
			if has != nil && (has(blob.SHA256) || has(hash)) {
				skipped++
				continue
			}
			if dryRun {
				planned++
				plannedSize += int64(blob.Size)
				fmt.Printf("%s\t%s\t%s\n", server.URL, hash, humanBytes(int64(blob.Size)))
				continue
			}
			bd, skip, err := mirrorTo(ctx, server, sk, sourceURL, hash, has)
			if err != nil {
				failed++
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", server.URL, hash, err)
				continue
			}
			if skip {
				skipped++
				continue
			}
			mirrored++
			printBlob(cCtx, bd)
		}
	}
	if dryRun {
		fmt.Fprintf(os.Stderr, "would mirror %d transfer(s), ~%s (skipped %d already present)\n", planned, humanBytes(plannedSize), skipped)
		return nil
	}
	if diff {
		fmt.Fprintf(os.Stderr, "mirrored %d, skipped %d, failed %d (of %d blobs)\n", mirrored, skipped, failed, len(bds))
	} else {
		fmt.Fprintf(os.Stderr, "mirrored %d, failed %d (of %d blobs)\n", mirrored, failed, len(bds))
	}
	if mirrored == 0 && failed > 0 {
		return errors.New("cannot mirror")
	}
	return nil
}

// sourceBlob identifies a single blob to mirror: its public URL and sha256.
type sourceBlob struct {
	url  string
	hash string
}

// resolveDests returns the destination servers: the positional URL arguments if
// any were given, otherwise the -s/config servers.
func resolveDests(cCtx *cli.Context, cfg *Config, destArgs []string) ([]fileServer, error) {
	if len(destArgs) > 0 {
		dests := make([]fileServer, len(destArgs))
		for i, d := range destArgs {
			dests[i] = parseFileServer(d)
		}
		return dests, nil
	}
	return fileServers(cCtx, cfg)
}

// mirrorBlobs mirrors each source blob into every destination server.
func mirrorBlobs(ctx context.Context, cCtx *cli.Context, dests []fileServer, sk string, sources []sourceBlob) error {
	if cCtx.Bool("dry-run") {
		var planned int
		var total int64
		for _, s := range sources {
			size := headSize(ctx, s.url)
			for _, server := range dests {
				planned++
				if size >= 0 {
					total += size
				}
				fmt.Printf("%s\t%s\t%s\n", server.URL, s.hash, humanBytes(size))
			}
		}
		fmt.Fprintf(os.Stderr, "would mirror %d transfer(s), ~%s\n", planned, humanBytes(total))
		return nil
	}
	var mirrored int
	for _, s := range sources {
		for _, server := range dests {
			bd, _, err := mirrorTo(ctx, server, sk, s.url, s.hash, nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %s: %v\n", server.URL, s.hash, err)
				continue
			}
			mirrored++
			printBlob(cCtx, bd)
		}
	}
	if mirrored == 0 {
		return errors.New("cannot mirror")
	}
	return nil
}

// headSize returns the Content-Length of url via a HEAD request, or -1 if
// unknown.
func headSize(ctx context.Context, url string) int64 {
	resp, err := httpHead(ctx, url)
	if err != nil || resp.StatusCode >= 300 {
		return -1
	}
	return resp.ContentLength
}

// humanBytes formats a byte count for display, or "?" when unknown (negative).
func humanBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func doFileMirror(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	sk, _, err := getSkAndPub(cfg)
	if err != nil {
		return err
	}
	from := cCtx.String("from")
	ctx := context.Background()
	args := cCtx.Args().Slice()

	// Source given via --from. Positional args (if any) are destination servers
	// (URLs) and/or specific blob hashes (sha256) to mirror. With neither, every
	// blob is mirrored (--all) or only those missing on each destination (diff,
	// the default). The source may be Blossom or NIP-96 (--nip96 / nip96+ prefix).
	if from != "" {
		source := parseFileServer(from)
		if cCtx.Bool("nip96") {
			source.Type = typeNIP96
		}
		var hashes, destArgs []string
		for _, a := range args {
			if nostr.IsValid32ByteHex(a) {
				hashes = append(hashes, a)
			} else {
				destArgs = append(destArgs, a)
			}
		}
		dests, err := resolveDests(cCtx, cfg, destArgs)
		if err != nil {
			return err
		}
		if len(hashes) > 0 {
			sources := make([]sourceBlob, len(hashes))
			for i, h := range hashes {
				sources[i] = sourceBlob{url: normalizeServer(source.URL) + "/" + h, hash: h}
			}
			return mirrorBlobs(ctx, cCtx, dests, sk, sources)
		}
		return mirrorFromSource(ctx, cCtx, cfg, dests, sk, source, !cCtx.Bool("all"))
	}

	// No --from: each positional <source-url> is mirrored into the -s/config
	// destination server(s).
	if len(args) == 0 {
		return errors.New("mirror needs a <source-url> argument, or --from <source server>")
	}
	dests, err := fileServers(cCtx, cfg)
	if err != nil {
		return err
	}
	sources := make([]sourceBlob, len(args))
	for i, src := range args {
		hash := hashFromURL(src)
		if !nostr.IsValid32ByteHex(hash) {
			return fmt.Errorf("cannot derive sha256 from %q (use --from <server> for a bare hash)", src)
		}
		sources[i] = sourceBlob{url: src, hash: hash}
	}
	return mirrorBlobs(ctx, cCtx, dests, sk, sources)
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
					&cli.StringFlag{Name: "from", Usage: "source server; positional args then become dest servers and/or sha256s to mirror"},
					&cli.BoolFlag{Name: "all", Usage: "with --from, mirror all blobs (default: only those missing on the dest)"},
					&cli.BoolFlag{Name: "nip96", Usage: "treat --from as a NIP-96 server when listing"},
					&cli.BoolFlag{Name: "dry-run", Aliases: []string{"n"}, Usage: "list what would be mirrored and the total size, without transferring"},
					&cli.BoolFlag{Name: "json", Usage: "output JSON blob descriptor"},
				},
				Usage:     "mirror blob(s) into the media server(s)",
				UsageText: "algia file mirror <source-url> [-s <dest>...] | --from <src> [<dest-url>... | <sha256>...] [--all]",
				ArgsUsage: "[source-url | dest-url... | sha256...]",
				Action:    doFileMirror,
			},
		},
	}
}

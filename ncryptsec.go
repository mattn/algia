package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip49"
	"github.com/urfave/cli/v2"
	"golang.org/x/term"
)

// passwordEnv lets scripts supply the NIP-49 password without a terminal.
const passwordEnv = "ALGIA_PASSWORD"

// isEncryptedKey reports whether key is a NIP-49 encrypted private key.
func isEncryptedKey(key string) bool {
	return strings.HasPrefix(key, "ncryptsec1")
}

// readPassword reads a password from the controlling terminal without echo.
// The terminal is opened directly so it works even when stdin is piped
// (e.g. "algia post --stdin").
func readPassword(prompt string) (string, error) {
	name := "/dev/tty"
	if runtime.GOOS == "windows" {
		name = "CONIN$"
	}
	tty, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return "", fmt.Errorf("no terminal to read the password from; set %s: %w", passwordEnv, err)
	}
	defer tty.Close()
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(tty.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// decryptKey decrypts a NIP-49 ncryptsec into a hex private key, taking the
// password from $ALGIA_PASSWORD or asking for it on the terminal.
func decryptKey(ncryptsec string) (string, error) {
	password, ok := os.LookupEnv(passwordEnv)
	if !ok {
		var err error
		if password, err = readPassword("password for ncryptsec: "); err != nil {
			return "", err
		}
	}
	sk, err := nip49.Decrypt(ncryptsec, password)
	if err != nil {
		return "", fmt.Errorf("cannot decrypt ncryptsec (wrong password?): %w", err)
	}
	return sk, nil
}

// keyCache remembers the public key of an encrypted private key so that
// read-only commands do not have to ask for the password.
type keyCache struct {
	Key    string `json:"key"` // sha256 of the ncryptsec
	PubKey string `json:"pubkey"`
}

func keyCachePath(profile string) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	name := "ncryptsec-pubkey.json"
	if profile != "" {
		name = "ncryptsec-pubkey-" + profile + ".json"
	}
	return filepath.Join(dir, "algia", name), nil
}

func keyCacheID(ncryptsec string) string {
	sum := sha256.Sum256([]byte(ncryptsec))
	return hex.EncodeToString(sum[:])
}

func loadCachedPubkey(profile, ncryptsec string) string {
	fp, err := keyCachePath(profile)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(fp)
	if err != nil {
		return ""
	}
	var c keyCache
	if json.Unmarshal(b, &c) != nil || c.Key != keyCacheID(ncryptsec) || !nostr.IsValidPublicKey(c.PubKey) {
		return ""
	}
	return c.PubKey
}

func saveCachedPubkey(profile, ncryptsec, pub string) error {
	fp, err := keyCachePath(profile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fp), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(keyCache{Key: keyCacheID(ncryptsec), PubKey: pub}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(fp, b, 0600)
}

func keyCommand() *cli.Command {
	return &cli.Command{
		Name:  "key",
		Usage: "private key utilities",
		Subcommands: []*cli.Command{
			{
				Name:      "encrypt",
				Usage:     "encrypt the profile's nsec with a password (NIP-49) and print the ncryptsec",
				UsageText: "algia [-a profile] key encrypt [--logn N]",
				HelpName:  "encrypt",
				Flags: []cli.Flag{
					&cli.UintFlag{Name: "logn", Value: 16, Usage: "scrypt cost as log2(N); higher is slower and stronger"},
				},
				Action: doKeyEncrypt,
			},
		},
	}
}

func doKeyEncrypt(cCtx *cli.Context) error {
	cfg := cCtx.App.Metadata["config"].(*Config)
	if isEncryptedKey(cfg.PrivateKey) {
		return errors.New("privatekey is already an ncryptsec")
	}
	sk, err := cfg.localSecretKey()
	if err != nil {
		return err
	}
	logn := cCtx.Uint("logn")
	if logn < 1 || logn > 22 {
		return errors.New("--logn must be between 1 and 22")
	}

	password, ok := os.LookupEnv(passwordEnv)
	if !ok {
		if password, err = readPassword("new password: "); err != nil {
			return err
		}
		again, err := readPassword("confirm password: ")
		if err != nil {
			return err
		}
		if password != again {
			return errors.New("passwords do not match")
		}
	}
	if password == "" {
		return errors.New("password must not be empty")
	}

	ncryptsec, err := nip49.Encrypt(sk, password, uint8(logn), nip49.ClientDoesNotTrackThisData)
	if err != nil {
		return err
	}
	fmt.Println(ncryptsec)
	fmt.Fprintln(os.Stderr, `replace "privatekey" in your config with the value above`)
	return nil
}

package edgedeploy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// RecoveryOptions contains only local configuration and the API token from CLOUDFLARE_API_TOKEN. The recovery token is
// created for this call and is never added to the deployment file.
type RecoveryOptions struct {
	StatePath  string
	Token      string
	AdminLogin string
	DryRun     bool
	APIBaseURL string
	HTTPClient *http.Client
	Random     io.Reader
}

// ResetLogin puts a fresh one-time recovery-token hash on the admin Worker and prints the matching link once.
func ResetLogin(ctx context.Context, opts RecoveryOptions, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if opts.Token == "" {
		return errors.New("CLOUDFLARE_API_TOKEN is required")
	}
	if opts.StatePath == "" {
		return errors.New("--file KEYFILE is required")
	}
	state, ok, err := loadExistingState(opts.StatePath)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("local deployment file %s does not exist", opts.StatePath)
	}
	if err := validateState(state); err != nil {
		return fmt.Errorf("local deployment file: %w", err)
	}
	adminScript := state.Scripts["admin"].Name
	if opts.DryRun {
		fmt.Fprintf(out, "Would upload secret RECOVERY_TOKEN_SHA256 to the admin Worker (%s). No link was created.\n", adminScript)
		return nil
	}
	random := opts.Random
	if random == nil {
		random = rand.Reader
	}
	token, err := newRecoveryToken(random)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(token))
	hashText := hex.EncodeToString(hash[:])
	api := newCFClient(opts.APIBaseURL, opts.Token, opts.HTTPClient)
	api.addSecrets(token, hashText)
	path := "/accounts/" + url.PathEscape(state.AccountID) + "/workers/scripts/" + url.PathEscape(adminScript) + "/secrets"
	secret := map[string]string{"name": "RECOVERY_TOKEN_SHA256", "text": hashText, "type": "secret_text"}
	if err := api.json(ctx, http.MethodPut, path, secret, nil); err != nil {
		if cloudflareHasErrorCode(err, 10215) {
			return errors.New("the latest admin Worker version is not deployed; deploy first, then run `mistgate edge reset-login` again")
		}
		return fmt.Errorf("upload recovery token secret to the admin Worker: %w", authFailure(err))
	}
	link := recoveryLink(state, opts.AdminLogin, token)
	fmt.Fprintln(out, link)
	return nil
}

func newRecoveryToken(r io.Reader) (string, error) {
	var raw [32]byte
	if _, err := io.ReadFull(r, raw[:]); err != nil {
		return "", fmt.Errorf("generate recovery token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func recoveryLink(state localState, login, token string) string {
	link := state.PublicURL + state.AdminPrefix + "recover"
	fragment := "recoveryToken=" + url.QueryEscape(token)
	if login != "" {
		fragment += "&login=" + url.QueryEscape(strings.TrimSpace(login))
	}
	return link + "#" + fragment
}

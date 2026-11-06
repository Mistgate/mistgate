package backup

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"github.com/mistgate/mistgate/internal/panel/store"
	"golang.org/x/term"
)

const usage = `usage: mistgate backup <keygen|restore> [flags]

  keygen  --identity-file FILE
          make the private recovery identity (once, offline) and print its public recipient
  restore (--identity-file FILE | --passphrase | --passphrase-file FILE)
          --file BACKUP.tar.gz.age --data-dir NEW-DIR
          decrypt a downloaded backup into a new data directory, offline

"mistgate backup <command> -h" lists the command's flags.
`

// RunCLI runs the offline backup recovery commands. The private identity is read
// only by restore and is never sent to the running panel or an object store.
func RunCLI(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New(strings.TrimSpace(usage))
	}
	switch args[0] {
	case "keygen":
		return runKeygen(args[1:], out)
	case "restore":
		return runRestore(args[1:], out)
	case "-h", "-help", "--help", "help":
		_, err := io.WriteString(out, usage)
		return err
	default:
		return fmt.Errorf("mistgate backup: unknown command %q (use keygen or restore)", args[0])
	}
}

func runKeygen(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backup keygen", flag.ContinueOnError)
	fs.SetOutput(out)
	identityPath := fs.String("identity-file", "", "path for the private age recovery identity (created once, mode 0600)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *identityPath == "" || fs.NArg() != 0 {
		return errors.New("usage: mistgate backup keygen --identity-file <private-key-file>")
	}
	path, err := filepath.Abs(*identityPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create recovery identity: %w", err)
	}
	recipient, writeErr := generateRecoveryKey(f)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.Join(writeErr, closeErr)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Recovery identity created at %s\nKeep this private file outside the panel.\nR2 recipient: %s\n", path, recipient)
	return err
}

func runRestore(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	fs.SetOutput(out)
	identityPath := fs.String("identity-file", "", "private age recovery identity created with backup keygen")
	usePassphrase := fs.Bool("passphrase", false, "prompt for an age scrypt passphrase without echo (requires a terminal)")
	passphrasePath := fs.String("passphrase-file", "", "read the age scrypt passphrase from the file's first line")
	archivePath := fs.String("file", "", "downloaded encrypted .tar.gz.age backup")
	dataDir := fs.String("data-dir", "", "new, non-existing directory for the restored panel data")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	methods := 0
	if *identityPath != "" {
		methods++
	}
	if *usePassphrase {
		methods++
	}
	if *passphrasePath != "" {
		methods++
	}
	if methods != 1 || *archivePath == "" || *dataDir == "" || fs.NArg() != 0 {
		return errors.New("usage: mistgate backup restore (--identity-file <private-key-file> | --passphrase | --passphrase-file <file>) --file <backup.tar.gz.age> --data-dir <new-directory>")
	}
	var identities []age.Identity
	var err error
	if *identityPath != "" {
		identityFile, err := os.Open(strings.TrimSpace(*identityPath))
		if err != nil {
			return fmt.Errorf("open recovery identity: %w", err)
		}
		parsed, parseErr := age.ParseIdentities(identityFile)
		closeErr := identityFile.Close()
		if parseErr != nil || closeErr != nil || len(parsed) == 0 {
			return errors.New("backup: cannot read a valid recovery identity")
		}
		identities = parsed
	} else {
		var passphrase string
		if *usePassphrase {
			passphrase, err = promptPassphrase()
		} else {
			passphrase, err = readPassphraseFile(*passphrasePath)
		}
		if err != nil {
			return err
		}
		identity, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			return errors.New("backup: cannot create passphrase identity")
		}
		identities = []age.Identity{identity}
	}
	archive, err := os.Open(*archivePath)
	if err != nil {
		return fmt.Errorf("open encrypted backup: %w", err)
	}
	info, statErr := archive.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() < 1 {
		archive.Close()
		return errors.New("backup: archive file is invalid")
	}
	restoreErr := RestoreEncryptedArchive(context.Background(), archive, identities, *dataDir)
	archiveCloseErr := archive.Close()
	if restoreErr != nil {
		return restoreErr
	}
	if archiveCloseErr != nil {
		return archiveCloseErr
	}
	panelURL := restoredPanelURL(context.Background(), *dataDir)
	_, err = fmt.Fprintf(out, "Panel data restored to %s\nBefore starting Mistgate, update any external systemd master.key credential to use the restored key.\n", *dataDir)
	if err == nil && panelURL != "" {
		_, err = fmt.Fprintf(out, "Panel URL: %s\n", panelURL)
	}
	return err
}

func restoredPanelURL(ctx context.Context, dataDir string) string {
	st, err := store.Open(ctx, filepath.Join(dataDir, databaseName))
	if err != nil {
		return ""
	}
	defer st.Close()
	values, err := st.SettingValues(ctx, []string{"public_url", "admin_host", "admin_prefix"})
	if err != nil {
		return ""
	}
	public, err := url.Parse(values["public_url"])
	if err != nil || public.Scheme == "" {
		return ""
	}
	if values["admin_host"] != "" {
		return public.Scheme + "://" + values["admin_host"] + "/"
	}
	return strings.TrimRight(values["public_url"], "/") + values["admin_prefix"]
}

const maxPassphraseFileBytes = 64 << 10

func promptPassphrase() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("backup restore: --passphrase requires an interactive terminal")
	}
	if _, err := fmt.Fprint(os.Stderr, "Backup passphrase: "); err != nil {
		return "", err
	}
	secret, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", errors.New("backup restore: could not read passphrase from terminal")
	}
	passphrase := string(secret)
	clear(secret)
	return passphrase, nil
}

func readPassphraseFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open passphrase file: %w", err)
	}
	defer file.Close()
	line, err := bufio.NewReader(io.LimitReader(file, maxPassphraseFileBytes+1)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("backup restore: could not read passphrase file")
	}
	if len(line) > maxPassphraseFileBytes {
		return "", errors.New("backup restore: passphrase file's first line is too long")
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	return line, nil
}

package backup

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

const usage = `usage: mistgate backup <keygen|restore> [flags]

  keygen  --identity-file FILE
          make the private recovery identity (once, offline) and print its public recipient
  restore --identity-file FILE --file BACKUP.tar.gz.age --data-dir NEW-DIR
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
	archivePath := fs.String("file", "", "downloaded encrypted .tar.gz.age backup")
	dataDir := fs.String("data-dir", "", "new, non-existing directory for the restored panel data")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *identityPath == "" || *archivePath == "" || *dataDir == "" || fs.NArg() != 0 {
		return errors.New("usage: mistgate backup restore --identity-file <private-key-file> --file <backup.tar.gz.age> --data-dir <new-directory>")
	}
	identityPathValue := strings.TrimSpace(*identityPath)
	identityFile, err := os.Open(identityPathValue)
	if err != nil {
		return fmt.Errorf("open recovery identity: %w", err)
	}
	identities, parseErr := age.ParseIdentities(identityFile)
	closeErr := identityFile.Close()
	if parseErr != nil || closeErr != nil || len(identities) == 0 {
		return errors.New("backup: cannot read a valid recovery identity")
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
	closeErr = archive.Close()
	if restoreErr != nil {
		return restoreErr
	}
	if closeErr != nil {
		return closeErr
	}
	_, err = fmt.Fprintf(out, "Panel data restored to %s\nBefore starting Mistgate, update any external systemd master.key credential to use the restored key.\n", *dataDir)
	return err
}

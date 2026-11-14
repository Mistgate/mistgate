//go:build linux

package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/mistgate/mistgate/internal/release"
)

const panelUpdateChannelMaxSize = 32

// readRootAllowedPanelUpdateChannel reads the policy outside the panel's writable data directory. A missing policy
// keeps existing installations on stable; any present file must sit in a root-owned directory nobody else can write,
// and be a small, root-owned regular file with no write permission for group or other users. The directory is opened
// first and the file is opened relative to it with O_NOFOLLOW, and both are checked through their descriptors, so the
// answer is the one for the file that is actually read.
func readRootAllowedPanelUpdateChannel(path string) (release.Channel, error) {
	dirFD, err := syscall.Open(filepath.Dir(path), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return release.ChannelStable, nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: open %s: %w", ErrPanelUpdateChannelConfig, filepath.Dir(path), err)
	}
	dir := os.NewFile(uintptr(dirFD), filepath.Dir(path))
	defer dir.Close()
	fd, err := syscall.Openat(dirFD, filepath.Base(path), syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return release.ChannelStable, nil
	}
	if err != nil {
		return "", fmt.Errorf("%w: open %s without following symlinks: %w", ErrPanelUpdateChannelConfig, path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	if err := requireRootOnlyWritable(dir, "directory"); err != nil {
		return "", err
	}
	if err := requireRootOnlyWritable(f, "file"); err != nil {
		return "", err
	}
	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("%w: stat %s: %w", ErrPanelUpdateChannelConfig, path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s must be a regular file", ErrPanelUpdateChannelConfig, path)
	}
	if info.Size() > panelUpdateChannelMaxSize {
		return "", fmt.Errorf("%w: %s is too large", ErrPanelUpdateChannelConfig, path)
	}

	body, err := io.ReadAll(io.LimitReader(f, panelUpdateChannelMaxSize+1))
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %w", ErrPanelUpdateChannelConfig, path, err)
	}
	if len(body) > panelUpdateChannelMaxSize {
		return "", fmt.Errorf("%w: %s is too large", ErrPanelUpdateChannelConfig, path)
	}
	channel, ok := parsePanelUpdateAllowedChannel(body)
	if !ok {
		return "", fmt.Errorf("%w: %s must contain stable, testing or dev", ErrPanelUpdateChannelConfig, path)
	}
	return channel, nil
}

// requireRootOnlyWritable fails unless the opened file or directory is owned by root and not writable by group or other.
func requireRootOnlyWritable(f *os.File, what string) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("%w: stat %s %s: %w", ErrPanelUpdateChannelConfig, what, f.Name(), err)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%w: %s %s must not be group- or world-writable", ErrPanelUpdateChannelConfig, what, f.Name())
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != 0 {
		return fmt.Errorf("%w: %s %s must be owned by root", ErrPanelUpdateChannelConfig, what, f.Name())
	}
	return nil
}

func parsePanelUpdateAllowedChannel(body []byte) (release.Channel, bool) {
	if len(body) > 0 && body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}
	channel, ok := release.ParseChannel(string(body))
	return channel, ok
}

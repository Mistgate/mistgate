package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/asndb"
)

const (
	defaultASNIPv4 = "https://iptoasn.com/data/ip2asn-v4-u32.tsv.gz"
	defaultASNIPv6 = "https://iptoasn.com/data/ip2asn-v6.tsv.gz"
)

// releaseASNDB builds the release-pinned table from local TSV files or HTTPS sources.
func releaseASNDB(args []string, out io.Writer, now time.Time) error {
	fs := flag.NewFlagSet("release asndb", flag.ContinueOnError)
	v4Path := fs.String("v4", defaultASNIPv4, "ip2asn IPv4 TSV path or HTTPS URL")
	v6Path := fs.String("v6", defaultASNIPv6, "ip2asn IPv6 TSV path or HTTPS URL")
	outPath := fs.String("out", "", "output table file (for example mistgate-asndb-data-any)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outPath == "" || fs.NArg() != 0 {
		return errors.New("usage: mistgate release asndb [--v4 PATH_OR_HTTPS_URL] [--v6 PATH_OR_HTTPS_URL] --out FILE")
	}
	return buildASNDB(*v4Path, *v6Path, *outPath, now, asndb.DefaultReleaseLimits(), out)
}

func buildASNDB(v4Path, v6Path, outPath string, now time.Time, limits asndb.ReleaseLimits, out io.Writer) error {
	table := asndb.Table{}
	var sourceTimes []time.Time
	for _, source := range []struct {
		path   string
		family asndb.Family
		label  string
	}{{v4Path, asndb.FamilyIPv4, "IPv4"}, {v6Path, asndb.FamilyIPv6, "IPv6"}} {
		input, modified, err := openASNSource(source.path)
		if err != nil {
			return fmt.Errorf("asndb %s source: %w", source.label, err)
		}
		parseErr := asndb.AppendTSV(input, source.family, &table)
		closeErr := input.Close()
		if parseErr != nil {
			return fmt.Errorf("asndb %s source: %w", source.label, parseErr)
		}
		if closeErr != nil {
			return fmt.Errorf("asndb %s source: close: %w", source.label, closeErr)
		}
		sourceTimes = append(sourceTimes, modified)
	}
	table.Built = sourceTimes[0]
	if sourceTimes[1].Before(table.Built) {
		table.Built = sourceTimes[1]
	}
	if table.Built.After(now.Add(5 * time.Minute)) {
		return fmt.Errorf("asndb source timestamp %s is in the future", table.Built.Format(time.RFC3339))
	}
	if limits.MaxAge <= 0 {
		return errors.New("asndb: invalid maximum source age")
	}
	if age := now.Sub(table.Built); age > limits.MaxAge {
		return fmt.Errorf("asndb source timestamp %s is older than %s", table.Built.Format(time.RFC3339), limits.MaxAge)
	}
	outputDir := filepath.Dir(outPath)
	tmp, err := os.CreateTemp(outputDir, ".mistgate-asndb-*")
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := asndb.Write(tmp, table); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("set output permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	check, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("reopen output: %w", err)
	}
	db, parseErr := asndb.Parse(check)
	closeErr := check.Close()
	if parseErr != nil {
		return fmt.Errorf("validate generated table: %w", parseErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close generated table: %w", closeErr)
	}
	if err := asndb.ValidateReleaseWith(db, now, limits); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return fmt.Errorf("install output: %w", err)
	}
	if _, err := fmt.Fprintf(out, "built ASN table from iptoasn.com, source date %s (%d IPv4 ranges, %d IPv6 /64 ranges, %d ASNs)\n",
		db.Built().Format("2006-01-02"), db.IPv4RangeCount(), db.IPv6RangeCount(), db.ASNCount()); err != nil {
		return fmt.Errorf("write command output: %w", err)
	}
	return nil
}

func openASNSource(source string) (io.ReadCloser, time.Time, error) {
	if strings.HasPrefix(strings.ToLower(source), "https://") {
		u, err := url.Parse(source)
		if err != nil || u.Host == "" || u.User != nil {
			return nil, time.Time{}, errors.New("invalid HTTPS URL")
		}
		client := &http.Client{
			Timeout: 5 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if !strings.EqualFold(req.URL.Scheme, "https") {
					return errors.New("HTTPS source redirected to a non-HTTPS URL")
				}
				return nil
			},
		}
		resp, err := client.Get(u.String())
		if err != nil {
			return nil, time.Time{}, err
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, time.Time{}, fmt.Errorf("HTTP status %s", resp.Status)
		}
		lastModified := resp.Header.Get("Last-Modified")
		if lastModified == "" {
			_ = resp.Body.Close()
			return nil, time.Time{}, errors.New("HTTPS source has no Last-Modified timestamp")
		}
		modified, err := http.ParseTime(lastModified)
		if err != nil {
			_ = resp.Body.Close()
			return nil, time.Time{}, fmt.Errorf("invalid Last-Modified timestamp: %w", err)
		}
		return resp.Body, modified.UTC(), nil
	}
	if strings.Contains(source, "://") {
		return nil, time.Time{}, errors.New("source URL must use HTTPS")
	}
	f, err := os.Open(source)
	if err != nil {
		return nil, time.Time{}, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, time.Time{}, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, time.Time{}, errors.New("source is not a regular file")
	}
	return f, info.ModTime().UTC(), nil
}

func validateASNDBFileWithLimits(path string, now time.Time, limits asndb.ReleaseLimits) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	db, parseErr := asndb.Parse(f)
	closeErr := f.Close()
	if parseErr != nil {
		return parseErr
	}
	if closeErr != nil {
		return closeErr
	}
	return asndb.ValidateReleaseWith(db, now, limits)
}

package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/node/update"
)

var (
	errRangedSize           = errors.New("ranged download size mismatch")
	errRangedOffsetTooLarge = errors.New("download offset is too large")
	errInvalidContentRange  = errors.New("invalid Content-Range")
)

type rangedGETPolicy struct {
	headers       http.Header
	userAgent     string
	strictTotal   bool
	expectedTotal uint64
	parseRange    func(string, uint64) (uint64, error)
	statusError   func(int) error
	requestError  func(error) error
	bodyReader    func(io.Reader) io.Reader
}

type activityReader struct {
	r     io.Reader
	touch func()
}

func (r activityReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 && r.touch != nil {
		r.touch()
	}
	return n, err
}

func parseRangedContentRange(value string, offset, expected uint64, strict bool) (uint64, error) {
	unit, value, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || unit != "bytes" {
		return 0, errInvalidContentRange
	}
	byteRange, totalText, ok := strings.Cut(value, "/")
	if !ok {
		return 0, errInvalidContentRange
	}
	startText, endText, ok := strings.Cut(byteRange, "-")
	if !ok {
		return 0, errInvalidContentRange
	}
	start, err := strconv.ParseUint(startText, 10, 64)
	if err != nil || start != offset {
		return 0, errInvalidContentRange
	}
	end, err := strconv.ParseUint(endText, 10, 64)
	if err != nil || end < start {
		return 0, errInvalidContentRange
	}
	if totalText == "*" && !strict {
		return 0, nil
	}
	total, err := strconv.ParseUint(totalText, 10, 64)
	if err != nil || total == 0 || end >= total || strict && total != expected {
		return 0, errInvalidContentRange
	}
	return total, nil
}

// rangedGET is the shared ranged HTTPS GET used by the link, GitHub and R2 update sources.
func rangedGET(ctx context.Context, client *http.Client, rawURL string, offset uint64, w io.Writer, policy rangedGETPolicy) (uint64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, update.PermanentSourceFailure("request", errors.New("cannot build ranged download request"))
	}
	req.Header.Set("Accept-Encoding", "identity")
	if policy.userAgent != "" {
		req.Header.Set("User-Agent", policy.userAgent)
	}
	for key, values := range policy.headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := client.Do(req)
	if err != nil {
		if policy.requestError != nil {
			return 0, policy.requestError(err)
		}
		return 0, err
	}
	defer resp.Body.Close()

	var total uint64
	switch resp.StatusCode {
	case http.StatusOK:
		if policy.strictTotal && (resp.ContentLength < 0 || uint64(resp.ContentLength) != policy.expectedTotal) {
			return 0, errRangedSize
		}
		if resp.ContentLength >= 0 {
			total = uint64(resp.ContentLength)
		}
	case http.StatusPartialContent:
		if policy.parseRange == nil {
			return 0, errInvalidContentRange
		}
		total, err = policy.parseRange(resp.Header.Get("Content-Range"), offset)
		if err != nil {
			return 0, err
		}
	default:
		if policy.statusError != nil {
			return 0, policy.statusError(resp.StatusCode)
		}
		return 0, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	var body io.Reader = resp.Body
	if policy.bodyReader != nil {
		body = policy.bodyReader(body)
	}
	if resp.StatusCode == http.StatusOK && offset > 0 {
		if offset > uint64(1<<63-1) {
			return total, update.PermanentSourceFailure("size", errRangedOffsetTooLarge)
		}
		// ponytail: draining an ignored Range prefix can cross the slow ceiling; GitHub and R2 honour Range.
		if _, err := io.CopyN(io.Discard, body, int64(offset)); err != nil {
			return total, fmt.Errorf("discard ranged response prefix: %w", err)
		}
	}
	if _, err := io.Copy(w, body); err != nil {
		return total, err
	}
	return total, nil
}

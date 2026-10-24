package agent

import (
	"sort"
	"sync"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/logscrub"
)

const (
	journalMaxEntries = 64
	journalMaxKinds   = journalMaxEntries - 1 // keep one slot for the overflow summary
)

var journalAttributeKeys = [...]string{
	"inbound", "protocol", "profile", "port", "state", "reason", "code", "backend", "mode", "version",
	"kernel", "iface", "check", "fix", "count", "err", "error",
}

type journalKey struct {
	level     pb.Severity
	source    string
	message   string
	inboundID string
	code      string
}

type journalItem struct {
	key      journalKey
	attrs    map[string]string
	count    uint32
	first    time.Time
	last     time.Time
	overflow bool
}

// journal coalesces WARN+ records until buildBatch takes the current interval.
// It never calls the logger and holds its mutex only while touching the bounded map.
type journal struct {
	mu          sync.Mutex
	items       map[journalKey]*journalItem
	overflow    uint32
	overflowAt  time.Time
	overflowEnd time.Time
}

func newJournal() *journal {
	return &journal{items: make(map[journalKey]*journalItem)}
}

func (j *journal) note(rec logRecord) {
	if rec.level != pb.Severity_SEVERITY_WARNING && rec.level != pb.Severity_SEVERITY_ERROR {
		return
	}

	message := rec.msg
	if template, ok := rec.attrs["msg_template"]; ok {
		message = template
	}
	message = logscrub.Clip(logscrub.Addrs(message), 256)

	attrs := make(map[string]string, 8)
	for _, key := range journalAttributeKeys {
		if len(attrs) == 8 {
			break
		}
		value, ok := rec.attrs[key]
		if !ok {
			continue
		}
		attrs[key] = logscrub.ClipRunes(logscrub.Addrs(value), 160)
	}
	if len(attrs) == 0 {
		attrs = nil
	}

	inboundID := ""
	if attrs != nil {
		inboundID = attrs["inbound"]
	}
	key := journalKey{
		level: rec.level, source: rec.source, message: message, inboundID: inboundID,
	}
	if attrs != nil {
		key.code = attrs["code"]
	}
	at := rec.at
	if at.IsZero() {
		at = time.Now()
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	if item := j.items[key]; item != nil {
		item.count = incrementJournalCount(item.count)
		if at.Before(item.first) {
			item.first = at
		}
		if at.After(item.last) {
			item.last = at
		}
		return
	}
	if len(j.items) < journalMaxKinds {
		j.items[key] = &journalItem{key: key, attrs: attrs, count: 1, first: at, last: at}
		return
	}
	j.overflow = incrementJournalCount(j.overflow)
	if j.overflowAt.IsZero() || at.Before(j.overflowAt) {
		j.overflowAt = at
	}
	if j.overflowEnd.IsZero() || at.After(j.overflowEnd) {
		j.overflowEnd = at
	}
}

// take transfers the current interval to a StatsBatch and starts a fresh one.
// Offset is applied after detaching the immutable entries, so sorting and protobuf allocation do not hold the mutex.
func (j *journal) take(offset time.Duration) []*pb.JournalEntry {
	j.mu.Lock()
	items, overflow := j.items, j.overflow
	overflowAt, overflowEnd := j.overflowAt, j.overflowEnd
	j.items = make(map[journalKey]*journalItem)
	j.overflow, j.overflowAt, j.overflowEnd = 0, time.Time{}, time.Time{}
	j.mu.Unlock()

	if len(items) == 0 && overflow == 0 {
		return nil
	}
	rows := make([]journalItem, 0, len(items)+1)
	for _, item := range items {
		rows = append(rows, *item)
	}
	if overflow > 0 {
		rows = append(rows, journalItem{
			key: journalKey{
				level: pb.Severity_SEVERITY_WARNING, source: "agent", message: "journal_overflow",
			},
			count: overflow, first: overflowAt, last: overflowEnd, overflow: true,
		})
	}
	sort.Slice(rows, func(i, k int) bool {
		a, b := rows[i], rows[k]
		aFirst, bFirst := a.first.Add(offset).Unix(), b.first.Add(offset).Unix()
		if aFirst != bFirst {
			return aFirst < bFirst
		}
		if a.key != b.key {
			return journalKeyLess(a.key, b.key)
		}
		return !a.overflow && b.overflow
	})

	out := make([]*pb.JournalEntry, 0, len(rows))
	for _, item := range rows {
		out = append(out, &pb.JournalEntry{
			Level: item.key.level, Source: item.key.source, Message: item.key.message,
			InboundId: item.key.inboundID, Attrs: item.attrs, Count: item.count,
			FirstUnix: item.first.Add(offset).Unix(), LastUnix: item.last.Add(offset).Unix(),
		})
	}
	return out
}

func journalKeyLess(a, b journalKey) bool {
	if a.level != b.level {
		return a.level < b.level
	}
	if a.source != b.source {
		return a.source < b.source
	}
	if a.message != b.message {
		return a.message < b.message
	}
	if a.inboundID != b.inboundID {
		return a.inboundID < b.inboundID
	}
	return a.code < b.code
}

func incrementJournalCount(count uint32) uint32 {
	if count < ^uint32(0) {
		return count + 1
	}
	return count
}

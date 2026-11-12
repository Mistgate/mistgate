package osupdate

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/mistgate/mistgate/internal/logscrub"
)

const (
	journalBudget = 128 << 10 // bytes of command output streamed to the unit's journal per job
	journalTail   = 40        // lines of the rest kept per command and written when it ends
	journalLine   = 400
)

// journal is where the output of apt and dpkg goes, i.e. the journal of the job's unit. It is bounded (a budget per job,
// then only the last lines of each command) and cleaned of addresses. Nothing it holds is sent to the panel.
type journal struct {
	mu      sync.Mutex
	w       io.Writer
	written int
	partial string
	tail    []string
	dropped int
}

func newJournal(w io.Writer) *journal {
	if w == nil {
		w = io.Discard
	}
	return &journal{w: w}
}

func (j *journal) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	lines := strings.Split(j.partial+string(p), "\n")
	j.partial = lines[len(lines)-1]
	if len(j.partial) > 4*journalLine {
		j.partial = j.partial[len(j.partial)-journalLine:]
	}
	for _, l := range lines[:len(lines)-1] {
		j.line(l)
	}
	return len(p), nil
}

func (j *journal) line(l string) {
	l = logscrub.Addrs(strings.TrimRight(l, "\r"))
	if len(l) > journalLine {
		l = l[:journalLine]
	}
	if j.written+len(l)+1 <= journalBudget {
		j.written += len(l) + 1
		fmt.Fprintln(j.w, l)
		return
	}
	j.dropped++
	j.tail = append(j.tail, l)
	if len(j.tail) > journalTail {
		j.tail = j.tail[1:]
	}
}

// flush ends a command's output: a half line is written, and when the budget cut the stream, the last lines follow.
func (j *journal) flush() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.partial != "" {
		j.line(j.partial)
		j.partial = ""
	}
	if j.dropped > 0 {
		fmt.Fprintf(j.w, "... %d lines left out; the last ones:\n", j.dropped-len(j.tail))
		for _, l := range j.tail {
			fmt.Fprintln(j.w, l)
		}
		j.dropped, j.tail = 0, nil
	}
}

package update

import (
	"context"
	"slices"
	"strconv"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// What a profile that does not pass the checks after an update is held against, and what the step says about it.
//
// The baseline: when a step is sent (agent update or OS job) the panel records, next to the inbounds that are FAILED, the profiles
// whose newest check round failed at most probeSickAge before, as "probe:<inbound id>" in the step's pre_failed list (no new column:
// the list is read only by the gate, and an id with the prefix never equals an inbound id, so the hard inbound_failed rule still sees
// only the first kind). Such a profile was not working before anybody touched the node: the gate does not count its failed or missing
// round as the update's fault. A step recorded before this rule has no such entries and is judged as it always was.
const (
	probeSickPrefix = "probe:"
	probeSickAge    = 15 * time.Minute // three rounds of a healthy profile; an older round says nothing about the moment of the send
)

// Step params the gate adds (admin update.proto RolloutStep.params).
const (
	keySkippedProfiles = "skipped_profiles" // JSON ["name"]: profiles that did not work before the update and were not held against it
	keySkippedMore     = "skipped_more"
)

// lastRounds lists the newest round of each probeable inbound of the node; nothing when the health module cannot say.
func (s *Service) lastRounds(ctx context.Context, nodeID string) []store.CheckSample {
	l, ok := s.hl.(interface {
		NodeLastRounds(ctx context.Context, nodeID string) ([]store.CheckSample, error)
	})
	if !ok {
		return nil
	}
	rounds, err := l.NodeLastRounds(ctx, nodeID)
	if err != nil {
		return nil
	}
	return rounds
}

// preFailedOf is the baseline recorded when a step is sent: the inbounds that are FAILED now, and "probe:<id>" for the profiles whose
// newest check round failed within probeSickAge. old is kept when the inbounds cannot be read.
func (s *Service) preFailedOf(ctx context.Context, nodeID string, now time.Time, old []string) []string {
	in, err := s.st.FleetInbounds(ctx, nodeID, true)
	if err != nil {
		return old
	}
	var out []string
	for _, inbound := range in {
		if inbound.State == "failed" {
			out = append(out, inbound.ID)
		}
	}
	for _, r := range s.lastRounds(ctx, nodeID) {
		if r.Status != int(adminv1.CheckStatus_CHECK_STATUS_OK) && !r.At.Before(now.Add(-probeSickAge)) {
			out = append(out, probeSickPrefix+r.InboundID)
		}
	}
	return out
}

// withoutProbeSick splits the inbounds without an OK round into those the baseline holds against the update and those it excuses.
func withoutProbeSick(ids, preFailed []string) (held, excused []string) {
	for _, id := range ids {
		if slices.Contains(preFailed, probeSickPrefix+id) {
			excused = append(excused, id)
		} else {
			held = append(held, id)
		}
	}
	return held, excused
}

// profileNames are the names of the profiles, sorted and each cut short (an id with no name keeps no entry).
func profileNames(ids []string, label map[string]string) []string {
	var names []string
	for _, id := range ids {
		if name := label[id]; name != "" && !slices.Contains(names, name) {
			names = append(names, truncateRunes(name, gateNameLn))
		}
	}
	slices.Sort(names)
	return names
}

// shown cuts a list to gateShown entries and says how many were left out.
func shown[T any](list []T) ([]T, int) {
	n := min(len(list), gateShown)
	return list[:n], len(list) - n
}

// skippedProfileParams says which profiles were not held against the update because their checks were failing before it.
func skippedProfileParams(with map[string]string, ids []string, label map[string]string) {
	head, more := shown(profileNames(ids, label))
	with[keySkippedProfiles] = jsonStringList(head)
	if more > 0 {
		with[keySkippedMore] = strconv.Itoa(more)
	}
}

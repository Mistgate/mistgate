package update

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
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

// Step params the gate adds (admin update.proto RolloutStep.params; the pause and the rolled-back event carry the first two too).
// The readers (the MCP views, web/src/lib/updates.ts) use these keys and this shape: testdata/gate_params.json pins them for the web.
const (
	KeyFailedProfiles  = "failed_profiles"  // JSON [{"id","name","code"}]: the profiles that did not pass, with the error code of their last round
	KeyFailedMore      = "failed_more"      // how many more than listed
	KeySkippedProfiles = "skipped_profiles" // JSON ["name"]: profiles that did not work before the update and were not held against it
	KeySkippedMore     = "skipped_more"
)

// What a gate param shows of the profiles: at most GateShown entries (the rest is counted in "more"), each name cut to gateNameLn runes.
const (
	GateShown  = 5
	gateNameLn = 40
)

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func jsonStringList(in []string) string {
	b, _ := json.Marshal(in)
	return string(b)
}

// codeNoRound is the "code" of a profile that has had no check round since the node was back.
const codeNoRound = "no_round"

// FailedProfile is one entry of the KeyFailedProfiles list.
type FailedProfile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// lastRounds lists the newest round of each probeable inbound of the node; nothing when there is no health module or it cannot say
// (the update is then judged without the baseline, as every profile's fault, so the failure is logged).
func (s *Service) lastRounds(ctx context.Context, nodeID string) []store.CheckSample {
	if s.hl == nil {
		return nil
	}
	rounds, err := s.hl.NodeLastRounds(ctx, nodeID)
	if err != nil {
		s.log.Warn("update: last check rounds unreadable", "code", "update_last_rounds_unreadable", "node", nodeID, "err", err)
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

// shown cuts a list to GateShown entries and says how many were left out.
func shown[T any](list []T) ([]T, int) {
	n := min(len(list), GateShown)
	return list[:n], len(list) - n
}

// failedProfileParams are the step params that name the profiles still failing (ids), with the error code of the newest round of
// each: "no_round" when there is none since the node was back. The names come from the owner's own profile settings; the codes
// are the checker's short words, never an address or a key.
func (s *Service) failedProfileParams(ctx context.Context, nodeID string, ids []string, label map[string]string, since time.Time) map[string]string {
	if len(ids) == 0 {
		return nil
	}
	codes := map[string]string{}
	for _, r := range s.lastRounds(ctx, nodeID) {
		if r.At.Before(since) {
			continue
		}
		if r.Status != int(adminv1.CheckStatus_CHECK_STATUS_OK) {
			codes[r.InboundID] = agentCode(r.ErrorCode)
		}
	}
	list := make([]FailedProfile, 0, len(ids))
	for _, id := range ids {
		code := codes[id]
		if code == "" {
			code = codeNoRound
		}
		list = append(list, FailedProfile{ID: id, Name: truncateRunes(label[id], gateNameLn), Code: code})
	}
	slices.SortFunc(list, func(a, b FailedProfile) int {
		return strings.Compare(a.Name+"\x00"+a.ID, b.Name+"\x00"+b.ID)
	})
	head, more := shown(list)
	b, _ := json.Marshal(head)
	out := map[string]string{KeyFailedProfiles: string(b)}
	if more > 0 {
		out[KeyFailedMore] = strconv.Itoa(more)
	}
	return out
}

// skippedProfileParams says which profiles were not held against the update because their checks were failing before it.
func skippedProfileParams(with map[string]string, ids []string, label map[string]string) {
	head, more := shown(profileNames(ids, label))
	with[KeySkippedProfiles] = jsonStringList(head)
	if more > 0 {
		with[KeySkippedMore] = strconv.Itoa(more)
	}
}

// failedProfileNotes are the params of a decided step that go with it to the rolled-back event and to the pause.
func failedProfileNotes(params map[string]string) map[string]string {
	out := map[string]string{}
	for _, k := range []string{KeyFailedProfiles, KeyFailedMore} {
		if v := params[k]; v != "" {
			out[k] = v
		}
	}
	return out
}

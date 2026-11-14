package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"time"
)

// maxDelta clamps a single reported counter so a broken or hostile node cannot overflow SQLite integers
// (1 PiB per 10 s interval is beyond any real link).
const maxDelta = 1 << 50

// FleetTraffic is one TrafficDelta of a StatsBatch.
type FleetTraffic struct {
	CredID, InboundID string
	Up, Down          uint64
}

// FleetSessionRef is one open session of a StatsBatch (used for the hourly peaks and AWG device touches).
type FleetSessionRef struct {
	CredID, InboundID string
	ConnectedAt       time.Time
}

// FleetInboundCert is a certificate reported for one inbound in a StatsBatch.
type FleetInboundCert struct {
	InboundID, Pin string
	NotAfter       time.Time
}

// FleetStatsIn is one reliable StatsBatch to commit.
type FleetStatsIn struct {
	NodeID, Instance string
	Session          uint64
	Seq              uint64
	Now              time.Time
	HourStart        int64 // bucket hour (multiple of 3600), already clamped by the caller
	Traffic          []FleetTraffic
	Links            []FleetLink // connection counters (agent.proto LinkCounters), stored in the same hour bucket
	Networks         []FleetNetwork
	PersonNetworks   []FleetPersonNetwork
	NetworksDBSHA256 string // digest the agent has loaded, from StatsBatch.networks_db_sha256
	Journal          []FleetJournal
	JournalSecrets   []string // this panel's secret names and path prefixes, removed before the journal is stored
	Sessions         []FleetSessionRef
	Certs            []FleetInboundCert
	// Host is the batch's HostMetrics (nil = the agent sent none): folded into node_host_hour in the same batch.
	Host *HostSample
	// Live builds the session projection inside each guarded attempt. It must not mutate captured state.
	Live func(refs map[string]FleetCredRef) FleetLive
	// Span is the interval the node was credited for this batch; with it a credential's speed limit caps its delta
	// (speedCeiling). 0 = no cap, for callers that do not know it.
	Span time.Duration
}

// FleetCredRef is what a credential id resolves to (credentials are soft-deleted, so revoked ones resolve).
type FleetCredRef struct {
	UserID, DeviceID, Protocol string
	// OnNode: the credential's user belongs on this node by group, profile and node selection, or had traffic accepted
	// from it in the last nodeUserWindow. A disabled user, a revoked device or a switched-off inbound still count, so the
	// last batch a node sends after such a change is still booked. Zero when a ref is built without a lookup (tests).
	OnNode bool
	// RateBps is the strictest speed limit of the credential (its device's or its person's), 0 = unlimited.
	RateBps uint64
}

// nodeUserWindow is how long a user stays "on a node" after the last traffic the panel accepted from it there. It covers
// the longest outbox a node replays (about six hours) with a wide margin, so a person moved off a node still has the
// tail of their traffic booked.
const nodeUserWindow = 48 * time.Hour

// speedCeilingFactor and speedCeilingSlack give a credential's speed limit room: the node's shaper allows bursts and the
// agent's interval floats, so the cap is factor x limit x interval plus slack, and a delta above it is counted at the cap.
const (
	speedCeilingFactor = 2
	speedCeilingSlack  = 16 << 20
)

// speedCeiling is the most bytes (up and down together) a credential with this speed limit may be credited in span.
// 0 = no cap.
func speedCeiling(rateBps uint64, span time.Duration) uint64 {
	if rateBps == 0 || span <= 0 {
		return 0
	}
	ceiling := float64(rateBps)/8*span.Seconds()*speedCeilingFactor + speedCeilingSlack
	if ceiling >= 1<<62 {
		return 0 // beyond anything a node can carry: the node's own guard is the cap
	}
	return uint64(ceiling)
}

// strictestLimit is the smaller non-zero of the person's and the device's speed limit, 0 when neither is set.
func strictestLimit(person, device uint64) uint64 {
	if person == 0 || (device != 0 && device < person) {
		return device
	}
	return person
}

// capDelta lowers up and down together so that their sum is at most ceiling, keeping their proportion.
func capDelta(up, down, ceiling uint64) (uint64, uint64, bool) {
	if ceiling == 0 || satAdd(up, down) <= ceiling {
		return up, down, false
	}
	total := satAdd(up, down)
	hi, lo := bits.Mul64(up, ceiling)
	if hi >= total { // cannot happen while up <= total, kept so Div64 never panics
		return ceiling, 0, true
	}
	newUp, _ := bits.Div64(hi, lo, total)
	return newUp, ceiling - newUp, true
}

// FleetStatsOut is the outcome of IngestStats.
type FleetStatsOut struct {
	Duplicate  bool                    // seq already committed: nothing was written
	Refs       map[string]FleetCredRef // every credential of the batch that resolved
	InboundIDs []string                // live inbounds currently assigned to this node
	Skipped    int                     // deltas and link rows dropped: unknown credential, or inbound not on this node
	OffNode    int                     // deltas dropped: the credential resolves but its user does not belong on this node
	Clamped    int                     // deltas counted at their credential's speed-limit ceiling instead of the reported size
	Probe      int                     // deltas and link rows of the health probe system credential, ignored on purpose (not an anomaly)
	Users      []string                // users whose used_bytes grew (for the access module's status recompute)
}

type fleetStatsSums struct{ up, down uint64 }

type fleetStatsBucketWrite struct {
	UserID    string `json:"user_id"`
	NodeID    string `json:"node_id"`
	Protocol  string `json:"protocol"`
	HourStart int64  `json:"hour_start"`
	BytesUp   int64  `json:"bytes_up"`
	BytesDown int64  `json:"bytes_down"`
}

type fleetStatsHourWrite struct {
	NodeID      string `json:"node_id"`
	Protocol    string `json:"protocol"`
	HourStart   int64  `json:"hour_start"`
	BytesUp     int64  `json:"bytes_up"`
	BytesDown   int64  `json:"bytes_down"`
	PeakUsers   int    `json:"peak_users"`
	PeakDevices int    `json:"peak_devices"`
}

type fleetStatsUserWrite struct {
	UserID string `json:"user_id"`
	Bytes  int64  `json:"bytes"`
}

type fleetStatsDeviceWrite struct {
	DeviceID string `json:"device_id"`
	Bytes    int64  `json:"bytes"`
}

type fleetInboundCertWrite struct {
	InboundID string `json:"inbound_id"`
	Pin       string `json:"pin"`
	NotAfter  int64  `json:"not_after"`
}

type fleetDeviceTouchWrite struct {
	DeviceID   string `json:"device_id"`
	LastSeenAt int64  `json:"last_seen_at"`
	NotAfter   int64  `json:"not_after"`
	// ConnectedAt is the newest session a node saw on the device (any protocol); 0 leaves last_connected_at alone. LastSeenAt
	// moves only where NotAfter is newer than the stored one, so a touch with a zero NotAfter is for last_connected_at only.
	ConnectedAt int64 `json:"connected_at,omitempty"`
	// OSVersion and Client are what a subscription fetch reports about its device; a node's touch leaves them out.
	OSVersion string `json:"os_version,omitempty"`
	Client    string `json:"client,omitempty"`
}

// IngestStats commits one StatsBatch in one read batch and one guarded write batch together with node.last_seq
// (agent.proto "RELIABLE MESSAGES"): traffic buckets and hourly peaks, user usage, inbound certificates, device touches, and
// the connection counters.
// seq == 0 (not a reliable message) skips the dedup and the counter update.
func (s *Store) IngestStats(ctx context.Context, in FleetStatsIn) (FleetStatsOut, error) {
	type bucketKey struct{ user, protocol string }
	var out FleetStatsOut
	_, err := s.retryGuarded(ctx, func() ([]Stmt, error) {
		out = FleetStatsOut{Refs: map[string]FleetCredRef{}}
		var instance string
		var last uint64
		onNode := map[string]string{} // inbound id -> its protocol
		liveInboundIDs := map[string]struct{}{}
		creds := map[string]FleetCredRef{}
		probes := map[string]bool{}

		credIDs := make([]string, 0, len(in.Traffic)+len(in.Sessions))
		seenCreds := map[string]bool{}
		for _, d := range in.Traffic {
			if !seenCreds[d.CredID] {
				seenCreds[d.CredID] = true
				credIDs = append(credIDs, d.CredID)
			}
		}
		for _, se := range in.Sessions {
			if !seenCreds[se.CredID] {
				seenCreds[se.CredID] = true
				credIDs = append(credIDs, se.CredID)
			}
		}
		for _, l := range in.Links {
			if l.CredID != "" && !seenCreds[l.CredID] {
				seenCreds[l.CredID] = true
				credIDs = append(credIDs, l.CredID)
			}
		}
		for _, network := range in.PersonNetworks {
			if network.CredID != "" && !seenCreds[network.CredID] {
				seenCreds[network.CredID] = true
				credIDs = append(credIDs, network.CredID)
			}
		}

		r := reads{}
		r.add(func(rows [][]any) error {
			if len(rows) != 1 {
				return ErrNotFound
			}
			return batchRow(rows[0]).Scan(&instance, &last)
		}, `SELECT agent_instance_id, last_seq FROM node WHERE id = ?`, in.NodeID)
		r.add(func(rows [][]any) error {
			for _, row := range rows {
				var id, protocol string
				var live int
				if err := batchRow(row).Scan(&id, &protocol, &live); err != nil {
					return err
				}
				onNode[id] = protocol
				if live != 0 {
					liveInboundIDs[id] = struct{}{}
				}
			}
			return nil
		}, `SELECT i.id, p.protocol, 1 AS is_live FROM inbound i JOIN profile p ON p.id = i.profile_id WHERE i.node_id = ?
			UNION ALL
			SELECT t.inbound_id, t.protocol, 0 AS is_live FROM node_inbound_stats_tombstone t
			WHERE t.node_id = ? AND NOT EXISTS (SELECT 1 FROM inbound i WHERE i.id = t.inbound_id AND i.node_id = t.node_id)`, in.NodeID, in.NodeID)
		r.add(func(rows [][]any) error {
			for _, row := range rows {
				var id string
				var ref FleetCredRef
				var personBps, deviceBps int64
				if err := batchRow(row).Scan(&id, &ref.UserID, &ref.DeviceID, &ref.Protocol, &personBps, &deviceBps, &ref.OnNode); err != nil {
					return err
				}
				ref.RateBps = strictestLimit(uint64(max(personBps, 0)), uint64(max(deviceBps, 0)))
				creds[id] = ref
			}
			return nil
		}, `SELECT c.id, c.user_id, c.device_id, c.protocol, u.speed_limit_bps, u.device_speed_limit_bps,
				(EXISTS (SELECT 1 FROM inbound i
					JOIN profile p ON p.id = i.profile_id AND p.protocol = c.protocol AND (c.profile_id IS NULL OR c.profile_id = i.profile_id)
					JOIN user_group_profile gp ON gp.profile_id = p.id AND gp.group_id = u.group_id
					WHERE i.node_id = ?1 AND (u.all_nodes = 1 OR EXISTS (SELECT 1 FROM user_node un WHERE un.user_id = u.id AND un.node_id = ?1)))
				OR EXISTS (SELECT 1 FROM traffic_bucket t WHERE t.user_id = c.user_id AND t.node_id = ?1 AND t.protocol = c.protocol AND t.hour_start >= ?2)) AS on_node
			FROM device_credential c JOIN user u ON u.id = c.user_id
			WHERE c.id IN (SELECT value FROM json_each(?3))`, in.NodeID, unix(in.Now)-int64(nodeUserWindow/time.Second), jsonStrings(credIDs))
		r.add(func(rows [][]any) error {
			for _, row := range rows {
				var id string
				if err := batchRow(row).Scan(&id); err != nil {
					return err
				}
				probes[id] = true
			}
			return nil
		}, `SELECT cred_id FROM health_probe_cred WHERE cred_id IN (SELECT value FROM json_each(?))`, jsonStrings(credIDs))
		if err := r.run(ctx, s); err != nil {
			return nil, err
		}
		out.InboundIDs = make([]string, 0, len(liveInboundIDs))
		for id := range liveInboundIDs {
			out.InboundIDs = append(out.InboundIDs, id)
		}
		sort.Strings(out.InboundIDs)
		// An agent may only attach a journal record to one of this node's inbounds. Keep the record, but remove a
		// foreign or stale inbound id so it cannot be used to attribute a message to another node.
		if len(in.Journal) > 0 {
			journal := append([]FleetJournal(nil), in.Journal...)
			for i := range journal {
				if _, ok := onNode[journal[i].InboundID]; journal[i].InboundID != "" && !ok {
					journal[i].InboundID = ""
				}
			}
			in.Journal = journal
		}
		lastForInstance := last
		if instance != in.Instance {
			lastForInstance = 0 // another agent instance took over: its seq space starts fresh
		}
		if in.Seq != 0 && in.Seq <= lastForInstance {
			out.Duplicate = true
			return nil, nil
		}
		out.Refs = creds
		live := FleetLive{}
		if in.Live != nil {
			live = in.Live(creds)
		}

		perBucket := map[bucketKey]*fleetStatsSums{}
		perProto := map[string]*fleetStatsSums{}
		perUser := map[string]uint64{}
		perDevice := map[string]uint64{}
		for _, d := range in.Traffic {
			ref, ok := creds[d.CredID]
			if !ok && probes[d.CredID] {
				out.Probe++
				continue
			}
			if !ok || onNode[d.InboundID] != ref.Protocol { // also: the credential must be of the inbound's protocol
				out.Skipped++
				continue
			}
			// A node books traffic of its own users only. An inbound deleted since is not checked (its tail is booked as before).
			if _, live := liveInboundIDs[d.InboundID]; live && !ref.OnNode {
				out.OffNode++
				continue
			}
			up, down := min(d.Up, maxDelta), min(d.Down, maxDelta)
			var clamped bool
			if up, down, clamped = capDelta(up, down, speedCeiling(ref.RateBps, in.Span)); clamped {
				out.Clamped++
			}
			k := bucketKey{ref.UserID, ref.Protocol}
			if perBucket[k] == nil {
				perBucket[k] = &fleetStatsSums{}
			}
			perBucket[k].up = satAdd(perBucket[k].up, up)
			perBucket[k].down = satAdd(perBucket[k].down, down)
			if perProto[ref.Protocol] == nil {
				perProto[ref.Protocol] = &fleetStatsSums{}
			}
			perProto[ref.Protocol].up = satAdd(perProto[ref.Protocol].up, up)
			perProto[ref.Protocol].down = satAdd(perProto[ref.Protocol].down, down)
			perUser[ref.UserID] = satAdd(perUser[ref.UserID], satAdd(up, down))
			if ref.DeviceID != "" {
				perDevice[ref.DeviceID] = satAdd(perDevice[ref.DeviceID], satAdd(up, down))
			}
		}

		// Connection counters: the same filter as the traffic (the key resolves and is of the inbound's protocol), and a row of the
		// inbound itself (cred "") only for an inbound of this node. A probe key's row is not stored: its connects and rejects (one
		// per handshake that reached authentication) go to the inbound row as probe_handshakes, which the reads take off the
		// inbound's hellos and handshakes (migration 00077), and the packets the node sent to the check and lost go there as
		// probe_pkts_sent and probe_pkts_lost (00081).
		linkRows := make([]linkRow, 0, len(in.Links))
		for _, l := range in.Links {
			if l.zero() {
				continue
			}
			user := ""
			if l.CredID == "" {
				if _, ok := onNode[l.InboundID]; !ok {
					out.Skipped++
					continue
				}
			} else {
				ref, ok := creds[l.CredID]
				if !ok && probes[l.CredID] {
					out.Probe++
					if _, on := onNode[l.InboundID]; on && (l.Connects|l.Rejects != 0 || l.PktsSent|l.PktsLost != 0) {
						r := linkRow{inbound: l.InboundID}
						r.n[lcProbeHandshakes] = min(uint64(l.Connects)+uint64(l.Rejects), maxLinkCount)
						r.n[lcProbePktsSent], r.n[lcProbePktsLost] = min(l.PktsSent, maxLinkSum), min(l.PktsLost, maxLinkSum)
						linkRows = append(linkRows, r)
					}
					continue
				}
				if !ok || onNode[l.InboundID] != ref.Protocol {
					out.Skipped++
					continue
				}
				user = ref.UserID
			}
			linkRows = append(linkRows, l.row(user))
		}

		networkRows := make([]FleetNetwork, 0, len(in.Networks))
		for _, network := range in.Networks {
			if _, ok := onNode[network.InboundID]; !ok {
				out.Skipped++
				continue
			}
			network.Attempts = min(network.Attempts, uint32(maxLinkCount))
			network.Unfinished = min(network.Unfinished, uint32(maxLinkCount))
			network.Connects = min(network.Connects, uint32(maxLinkCount))
			network.Broken = min(network.Broken, uint32(maxLinkCount))
			network.People = min(network.People, uint32(maxLinkCount))
			if network.Attempts|network.Unfinished|network.Connects|network.Broken|network.People == 0 {
				continue
			}
			networkRows = append(networkRows, network)
		}
		personRows := make([]userNetworkWrite, 0, len(in.PersonNetworks))
		for _, network := range in.PersonNetworks {
			if probes[network.CredID] {
				out.Probe++
				continue
			}
			ref, ok := creds[network.CredID]
			if !ok || ref.UserID == "" || onNode[network.InboundID] != ref.Protocol {
				out.Skipped++
				continue
			}
			if network.Connects == 0 {
				continue
			}
			personRows = append(personRows, userNetworkWrite{UserID: ref.UserID, ASN: network.ASN,
				Connects: int64(min(network.Connects, uint32(maxLinkCount))), LastHour: in.HourStart})
		}

		// Peaks: distinct users / devices per protocol in this snapshot.
		users := map[string]map[string]bool{}
		devs := map[string]map[string]bool{}
		for _, se := range in.Sessions {
			ref, ok := creds[se.CredID]
			if !ok || onNode[se.InboundID] != ref.Protocol {
				continue
			}
			if users[ref.Protocol] == nil {
				users[ref.Protocol], devs[ref.Protocol] = map[string]bool{}, map[string]bool{}
			}
			users[ref.Protocol][ref.UserID] = true
			devs[ref.Protocol][ref.DeviceID] = true
		}

		bucketWrites := make([]fleetStatsBucketWrite, 0, len(perBucket))
		for k, v := range perBucket {
			bucketWrites = append(bucketWrites, fleetStatsBucketWrite{UserID: k.user, NodeID: in.NodeID, Protocol: k.protocol,
				HourStart: in.HourStart, BytesUp: i64(v.up), BytesDown: i64(v.down)})
		}
		sort.Slice(bucketWrites, func(i, j int) bool {
			if bucketWrites[i].UserID != bucketWrites[j].UserID {
				return bucketWrites[i].UserID < bucketWrites[j].UserID
			}
			return bucketWrites[i].Protocol < bucketWrites[j].Protocol
		})
		protos := map[string]bool{}
		for p := range perProto {
			protos[p] = true
		}
		for p := range users {
			protos[p] = true
		}
		protocolIDs := make([]string, 0, len(protos))
		for p := range protos {
			protocolIDs = append(protocolIDs, p)
		}
		sort.Strings(protocolIDs)
		hourWrites := make([]fleetStatsHourWrite, 0, len(protocolIDs))
		for _, p := range protocolIDs {
			var up, down uint64
			if v := perProto[p]; v != nil {
				up, down = v.up, v.down
			}
			hourWrites = append(hourWrites, fleetStatsHourWrite{NodeID: in.NodeID, Protocol: p, HourStart: in.HourStart,
				BytesUp: i64(up), BytesDown: i64(down), PeakUsers: len(users[p]), PeakDevices: len(devs[p])})
		}
		userIDs := make([]string, 0, len(perUser))
		for uid := range perUser {
			userIDs = append(userIDs, uid)
		}
		sort.Strings(userIDs)
		userWrites := make([]fleetStatsUserWrite, 0, len(userIDs))
		for _, uid := range userIDs {
			userWrites = append(userWrites, fleetStatsUserWrite{UserID: uid, Bytes: i64(perUser[uid])})
			out.Users = append(out.Users, uid)
		}
		deviceIDs := make([]string, 0, len(perDevice))
		for id := range perDevice {
			deviceIDs = append(deviceIDs, id)
		}
		sort.Strings(deviceIDs)
		deviceWrites := make([]fleetStatsDeviceWrite, 0, len(deviceIDs))
		for _, id := range deviceIDs {
			deviceWrites = append(deviceWrites, fleetStatsDeviceWrite{DeviceID: id, Bytes: i64(perDevice[id])})
		}
		certByInbound := make(map[string]FleetInboundCert, len(in.Certs))
		for _, cert := range in.Certs {
			if _, ok := liveInboundIDs[cert.InboundID]; !ok {
				continue
			}
			certByInbound[cert.InboundID] = cert
		}
		certIDs := make([]string, 0, len(certByInbound))
		for id := range certByInbound {
			certIDs = append(certIDs, id)
		}
		sort.Strings(certIDs)
		certWrites := make([]fleetInboundCertWrite, 0, len(certIDs))
		for _, id := range certIDs {
			cert := certByInbound[id]
			certWrites = append(certWrites, fleetInboundCertWrite{InboundID: cert.InboundID, Pin: cert.Pin, NotAfter: fleetUnix(cert.NotAfter)})
		}
		// Every session is a use of its device and moves last_connected_at, the evidence the idle rule reads (device_idle.go).
		// last_seen_at means something else for a link device, its last subscription fetch (the user page says so and the
		// health check compares it with the traffic), so only a key's own sessions move it: an AmneziaWG key's handshake, a
		// Telegram key's proxy session.
		touchByDevice := make(map[string]fleetDeviceTouchWrite)
		for _, se := range in.Sessions {
			ref, ok := creds[se.CredID]
			if !ok || ref.DeviceID == "" || se.ConnectedAt.Unix() <= 0 {
				continue
			}
			touchedAt := se.ConnectedAt
			if touchedAt.After(in.Now) {
				touchedAt = in.Now
			}
			touchedUnix := unix(touchedAt)
			touch := touchByDevice[ref.DeviceID]
			touch.DeviceID = ref.DeviceID
			touch.ConnectedAt = max(touch.ConnectedAt, touchedUnix)
			if ref.Protocol == "awg" || ref.Protocol == TelegramProtocol {
				touch.LastSeenAt = max(touch.LastSeenAt, touchedUnix)
				touch.NotAfter = touch.LastSeenAt
			}
			touchByDevice[ref.DeviceID] = touch
		}
		touchIDs := make([]string, 0, len(touchByDevice))
		for id := range touchByDevice {
			touchIDs = append(touchIDs, id)
		}
		sort.Strings(touchIDs)
		touchWrites := make([]fleetDeviceTouchWrite, 0, len(touchIDs))
		for _, id := range touchIDs {
			touchWrites = append(touchWrites, touchByDevice[id])
		}
		stmts, err := fleetStatsWriteStmts(in, instance, last, bucketWrites, hourWrites, userWrites, deviceWrites, certWrites,
			touchWrites, linkRows, networkRows, personRows, live)
		if err != nil {
			return nil, err
		}
		return stmts, nil
	})
	if err != nil {
		return FleetStatsOut{}, err
	}
	return out, nil
}

func fleetStatsWriteStmts(in FleetStatsIn, instance string, last uint64, buckets []fleetStatsBucketWrite,
	hours []fleetStatsHourWrite, users []fleetStatsUserWrite, devices []fleetStatsDeviceWrite, certs []fleetInboundCertWrite,
	touches []fleetDeviceTouchWrite, links []linkRow, networks []FleetNetwork, personNetworks []userNetworkWrite, live FleetLive) ([]Stmt, error) {
	bucketJSON, err := json.Marshal(buckets)
	if err != nil {
		return nil, err
	}
	hourJSON, err := json.Marshal(hours)
	if err != nil {
		return nil, err
	}
	userJSON, err := json.Marshal(users)
	if err != nil {
		return nil, err
	}
	deviceJSON, err := json.Marshal(devices)
	if err != nil {
		return nil, err
	}
	certStmt, err := inboundCertsStmt(in.NodeID, in.Now, certs)
	if err != nil {
		return nil, err
	}
	touchStmt, err := deviceTouchStmt(touches)
	if err != nil {
		return nil, err
	}
	linkStmt, err := linkHourStmt(in.NodeID, in.HourStart, links)
	if err != nil {
		return nil, err
	}
	networkStmt, err := networkHourStmt(in.NodeID, in.HourStart, networks)
	if err != nil {
		return nil, err
	}
	personStmt, err := userNetworkStmt(personNetworks)
	if err != nil {
		return nil, err
	}
	journalStmt, err := nodeJournalStmt(in.NodeID, in.HourStart, in.Journal, in.JournalSecrets)
	if err != nil {
		return nil, err
	}
	liveStmt, err := nodeLiveStatsStmt(in.NodeID, in.Session, live)
	if err != nil {
		return nil, err
	}
	stmts := []Stmt{
		guard(`EXISTS (SELECT 1 FROM node WHERE id = ? AND agent_instance_id = ? AND last_seq = ?)`, in.NodeID, instance, int64(last)),
		{Query: `
			INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down)
			SELECT json_extract(bucket.value, '$.user_id'), json_extract(bucket.value, '$.node_id'),
				json_extract(bucket.value, '$.protocol'), CAST(json_extract(bucket.value, '$.hour_start') AS INTEGER),
				CAST(json_extract(bucket.value, '$.bytes_up') AS INTEGER), CAST(json_extract(bucket.value, '$.bytes_down') AS INTEGER)
			FROM json_each(?1) AS bucket
			WHERE 1
			ON CONFLICT (user_id, node_id, protocol, hour_start) DO UPDATE SET
				bytes_up = bytes_up + excluded.bytes_up, bytes_down = bytes_down + excluded.bytes_down`, Args: []any{string(bucketJSON)}},
		{Query: `
			INSERT INTO node_traffic_hour (node_id, protocol, hour_start, bytes_up, bytes_down, peak_users, peak_devices)
			SELECT json_extract(hour.value, '$.node_id'), json_extract(hour.value, '$.protocol'),
				CAST(json_extract(hour.value, '$.hour_start') AS INTEGER), CAST(json_extract(hour.value, '$.bytes_up') AS INTEGER),
				CAST(json_extract(hour.value, '$.bytes_down') AS INTEGER), CAST(json_extract(hour.value, '$.peak_users') AS INTEGER),
				CAST(json_extract(hour.value, '$.peak_devices') AS INTEGER)
			FROM json_each(?1) AS hour
			WHERE 1
			ON CONFLICT (node_id, protocol, hour_start) DO UPDATE SET
				bytes_up = bytes_up + excluded.bytes_up, bytes_down = bytes_down + excluded.bytes_down,
				peak_users = max(peak_users, excluded.peak_users), peak_devices = max(peak_devices, excluded.peak_devices)`, Args: []any{string(hourJSON)}},
		{Query: `
			UPDATE user SET used_bytes = used_bytes + CAST(json_extract(change.value, '$.bytes') AS INTEGER),
				last_seen_at = ?2, last_node_id = ?3
			FROM json_each(?1) AS change
			WHERE user.id = json_extract(change.value, '$.user_id')`, Args: []any{string(userJSON), unix(in.Now), in.NodeID}},
		{Query: `
			UPDATE device SET used_bytes = used_bytes + CAST(json_extract(change.value, '$.bytes') AS INTEGER)
			FROM json_each(?1) AS change
			WHERE device.id = json_extract(change.value, '$.device_id')`, Args: []any{string(deviceJSON)}},
		certStmt,
		touchStmt,
		linkStmt,
		journalStmt,
		liveStmt,
	}
	if in.Host != nil {
		stmts = append(stmts, HostHourStmt(in.NodeID, in.HourStart, *in.Host))
	}
	if len(networks) > 0 && networkStmt.Query != "" {
		stmts = append(stmts, networkStmt)
	}
	if len(personNetworks) > 0 && personStmt.Query != "" {
		stmts = append(stmts, personStmt)
	}
	return append(stmts, commitStatsSeqStmt(in.NodeID, in.Instance, in.Seq, in.Now, in.NetworksDBSHA256)), nil
}

func nodeLiveStatsStmt(nodeID string, session uint64, live FleetLive) (Stmt, error) {
	sessionID, err := nodeSessionValue(session)
	if err != nil {
		return Stmt{}, err
	}
	if live.UsersJSON == "" {
		live.UsersJSON = "{}"
	}
	if live.DevicesJSON == "" {
		live.DevicesJSON = "{}"
	}
	if live.LiveJSON == "" {
		live.LiveJSON = "{}"
	}
	return Stmt{Query: `
		UPDATE node_live SET drift = ?, sample_at = ?, rx_bps = ?, tx_bps = ?, cpu_pct = ?, users = ?, devices = ?, live_json = ?
		WHERE node_id = ? AND session = ? AND ? = 1`,
		Args: []any{boolInt(live.Drift), live.SampleAt, int64(live.RxBps), int64(live.TxBps),
			live.CPUPct, live.UsersJSON, live.DevicesJSON, live.LiveJSON, nodeID, sessionID, boolInt(live.Apply)}}, nil
}

func inboundCertsStmt(nodeID string, now time.Time, certs []fleetInboundCertWrite) (Stmt, error) {
	encoded, err := json.Marshal(certs)
	if err != nil {
		return Stmt{}, err
	}
	return Stmt{Query: `
		UPDATE inbound SET cert_pin_sha256 = json_extract(cert.value, '$.pin'),
			cert_not_after = CAST(json_extract(cert.value, '$.not_after') AS INTEGER), updated_at = ?2
		FROM json_each(?1) AS cert
		WHERE inbound.id = json_extract(cert.value, '$.inbound_id') AND inbound.node_id = ?3 AND inbound.enabled = 1
			AND (inbound.cert_pin_sha256 != json_extract(cert.value, '$.pin') OR
				inbound.cert_not_after != CAST(json_extract(cert.value, '$.not_after') AS INTEGER))`,
		Args: []any{string(encoded), unix(now), nodeID}}, nil
}

func deviceTouchStmt(touches []fleetDeviceTouchWrite) (Stmt, error) {
	encoded, err := json.Marshal(touches)
	if err != nil {
		return Stmt{}, err
	}
	return Stmt{Query: `
		UPDATE device SET
			last_seen_at = CASE WHEN device.last_seen_at < CAST(json_extract(touch.value, '$.not_after') AS INTEGER)
				THEN CAST(json_extract(touch.value, '$.last_seen_at') AS INTEGER) ELSE device.last_seen_at END,
			last_connected_at = max(device.last_connected_at, coalesce(CAST(json_extract(touch.value, '$.connected_at') AS INTEGER), 0)),
			os_version = coalesce(nullif(json_extract(touch.value, '$.os_version'), ''), os_version),
			client = coalesce(nullif(json_extract(touch.value, '$.client'), ''), client)
		FROM json_each(?1) AS touch
		WHERE device.id = json_extract(touch.value, '$.device_id')
			AND (device.last_seen_at < CAST(json_extract(touch.value, '$.not_after') AS INTEGER)
				OR device.last_connected_at < coalesce(CAST(json_extract(touch.value, '$.connected_at') AS INTEGER), 0))`,
		Args: []any{string(encoded)}}, nil
}

func commitSeqStmt(nodeID, instance string, seq uint64, now time.Time) Stmt {
	if seq == 0 {
		return Stmt{Query: `UPDATE node SET last_seen_at = max(last_seen_at, ?) WHERE id = ?`, Args: []any{unix(now), nodeID}}
	}
	return Stmt{Query: `UPDATE node SET last_seq = ?, agent_instance_id = ?, last_seen_at = max(last_seen_at, ?) WHERE id = ?`,
		Args: []any{int64(seq), instance, unix(now), nodeID}}
}

func commitStatsSeqStmt(nodeID, instance string, seq uint64, now time.Time, networksDBSHA256 string) Stmt {
	if seq == 0 {
		return Stmt{Query: `UPDATE node SET last_seen_at = max(last_seen_at, ?), networks_db_sha256 = ? WHERE id = ?`,
			Args: []any{unix(now), networksDBSHA256, nodeID}}
	}
	return Stmt{Query: `UPDATE node SET last_seq = ?, agent_instance_id = ?, last_seen_at = max(last_seen_at, ?), networks_db_sha256 = ? WHERE id = ?`,
		Args: []any{int64(seq), instance, unix(now), networksDBSHA256, nodeID}}
}

// satAdd adds without wrapping around; i64 converts without turning a huge value negative. The fleet module
// filters implausible numbers before they get here (statsguard.go): these keep SQLite from ever being handed
// something it cannot store, whatever the caller does.
func satAdd(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}

func i64(n uint64) int64 { return int64(min(n, math.MaxInt64)) }

// SkipSeq marks the reliable message seq of the node's current agent instance as processed without applying
// its effects, for a batch that cannot be stored (the node gets its Ack and moves on).
func (s *Store) SkipSeq(ctx context.Context, nodeID, instance string, seq uint64, now time.Time) error {
	_, err := s.batch(ctx, commitSeqStmt(nodeID, instance, seq, now))
	return err
}

// Agent events stored per node and UTC day, one budget per class, so a flood of one class cannot hide another: an
// honest agent sends an info event per engine start, apply and restart (a link that flaps every minute makes about 1,500
// a day), warnings and errors only when something is wrong, and a torrent_attempt (a warning) at most once per inbound and
// person every five minutes, which is why that code has a budget of its own. Over the budget a new event is dropped (and
// acked) and one panel marker "node_event_daily_limit" with the class in its params is written for that node, class and day.
const (
	eventClassInfo    = "info"
	eventClassWarning = "warning"
	eventClassError   = "error"
	eventClassTorrent = "torrent"
)

var agentEventDailyLimits = map[string]int{eventClassInfo: 2000, eventClassWarning: 1000, eventClassError: 1000, eventClassTorrent: 1000}

// eventClassSQL is the class of a row of the event table, the same rule as agentEventClass.
const eventClassSQL = `CASE WHEN code = 'torrent_attempt' THEN 'torrent' WHEN severity = 1 THEN 'info' WHEN severity = 2 THEN 'warning' ELSE 'error' END`

func agentEventClass(e EventRow) string {
	switch {
	case e.Code == "torrent_attempt":
		return eventClassTorrent
	case e.Severity == 1:
		return eventClassInfo
	case e.Severity == 2:
		return eventClassWarning
	default:
		return eventClassError
	}
}

// IngestEvent stores one reliable agent Event and node.last_seq in one atomic batch. dup reports that the seq was already
// committed.
func (s *Store) IngestEvent(ctx context.Context, nodeID, instance string, seq uint64, now time.Time, e EventRow) (dup bool, err error) {
	dup, _, err = s.IngestEventBounded(ctx, nodeID, instance, seq, now, e)
	return dup, err
}

// IngestEventBounded is IngestEvent under the daily budget of the event's class; overLimit reports that the row was
// dropped for it (node.last_seq still moves, so the node is acked).
func (s *Store) IngestEventBounded(ctx context.Context, nodeID, instance string, seq uint64, now time.Time, e EventRow) (dup, overLimit bool, err error) {
	now = now.UTC()
	dayStart := now.Truncate(24 * time.Hour).Unix()
	dayEnd := now.Truncate(24 * time.Hour).Add(24 * time.Hour).Unix()
	e.NodeID, e.Source, e.SrcInstance, e.SrcSeq, e.ReceivedAt = nodeID, "agent", instance, seq, now
	class := agentEventClass(e)
	limit := agentEventDailyLimits[class]
	// Rows of this class that the node's agent has had stored today.
	used := `(SELECT count(*) FROM event WHERE source = 'agent' AND node_id = ? AND received_at >= ? AND received_at < ? AND ` + eventClassSQL + ` = ?)`
	stmts := []Stmt{
		{Query: `SELECT id FROM node WHERE id = ?`, Args: []any{nodeID}, Returning: true},
		agentEventInsertStmt(eventInsertArgs(e), used, nodeID, instance, seq, []any{nodeID, dayStart, dayEnd, class, limit}),
		// The marker is looked up first (one index probe), so the count runs only until the day's marker exists.
		{Query: `INSERT OR IGNORE INTO event (ts, received_at, severity, code, source, node_id, params_json)
			SELECT ?, ?, 2, 'node_event_daily_limit', 'panel', ?, ?
			WHERE NOT EXISTS (SELECT 1 FROM event WHERE source = 'panel' AND code = 'node_event_daily_limit' AND node_id = ?
				AND received_at / 86400 = ? AND json_extract(params_json, '$.level') = ?)
			AND ` + used + ` >= ?`,
			Args: []any{unix(now), unix(now), nodeID, `{"level":"` + class + `","limit":"` + fmt.Sprint(limit) + `"}`,
				nodeID, dayStart / 86400, class, nodeID, dayStart, dayEnd, class, limit}},
	}
	if seq == 0 {
		stmts = append(stmts, Stmt{Query: `UPDATE node SET last_seen_at = max(last_seen_at, ?) WHERE id = ? RETURNING id`,
			Args: []any{unix(now), nodeID}, Returning: true})
	} else {
		stmts = append(stmts, Stmt{Query: `UPDATE node SET last_seq = ?, agent_instance_id = ?, last_seen_at = max(last_seen_at, ?)
				WHERE id = ? AND (agent_instance_id <> ? OR last_seq < ?) RETURNING id`,
			Args: []any{int64(seq), instance, unix(now), nodeID, instance, int64(seq)}, Returning: true})
	}
	results, err := s.batch(ctx, stmts...)
	if err != nil {
		return false, false, err
	}
	if len(results[0].Rows) == 0 {
		return false, false, ErrNotFound
	}
	dup = len(results[len(results)-1].Rows) == 0
	overLimit = len(results[1].Rows) == 0 && !dup // not stored and not a repeat: the budget refused it
	return dup, overLimit, nil
}

func agentEventInsertStmt(args []any, used, nodeID, instance string, seq uint64, usedArgs []any) Stmt {
	args = append(args, nodeID, int64(seq), instance, int64(seq))
	args = append(args, usedArgs...)
	return Stmt{Query: `INSERT OR IGNORE INTO event (ts, received_at, severity, code, source, node_id, user_id, device_id, inbound_id, params_json, src_instance, src_seq)
		SELECT ?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM node WHERE id = ? AND (? = 0 OR agent_instance_id <> ? OR last_seq < ?))
		AND ` + used + ` < ?
		RETURNING id`, Args: args, Returning: true}
}

type fleetExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertEvent(ctx context.Context, ex fleetExecer, e EventRow) error {
	if e.ReceivedAt.IsZero() {
		e.ReceivedAt = e.Time
	}
	_, err := ex.ExecContext(ctx, `
		INSERT OR IGNORE INTO event (ts, received_at, severity, code, source, node_id, user_id, device_id, inbound_id, params_json, src_instance, src_seq)
		VALUES (?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, ?)`, eventInsertArgs(e)...)
	return err
}

func eventInsertArgs(e EventRow) []any {
	params := "{}"
	if len(e.Params) > 0 {
		b, _ := json.Marshal(e.Params)
		params = string(b)
	}
	var seq any
	var inst any
	if e.SrcSeq != 0 {
		seq, inst = int64(e.SrcSeq), e.SrcInstance
	}
	receivedAt := e.ReceivedAt
	if receivedAt.IsZero() {
		receivedAt = e.Time
	}
	return []any{unix(e.Time), unix(receivedAt), e.Severity, e.Code, e.Source, e.NodeID, e.UserID, e.DeviceID, e.InboundID, params, inst, seq}
}

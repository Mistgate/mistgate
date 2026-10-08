package health

import (
	"sort"
	"strconv"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// ponytail: starting points, calibrate on a real instance.
const (
	userAccessEndedFetchWindow = 6 * time.Hour
	userAccessEndedHoldWindow  = 48 * time.Hour
	userAccessEndedMaxAge      = 30 * 24 * time.Hour
	userNeverConnectedMinAge   = 2 * time.Hour
	userConnectionMaxAge       = 14 * 24 * time.Hour
	userStaleHandshakeWindow   = time.Hour
	userSilentFetchWindow      = 24 * time.Hour
	userSilentTrafficGap       = 2 * time.Hour
	userSilentTrafficMaxAge    = 3 * 24 * time.Hour
	userSilentTrafficMinAge    = 24 * time.Hour
	userImpactMinHourAge       = 20 * time.Minute
	userImpactNodeConnectGrace = 20 * time.Minute
	userImpactMinUsual         = uint64(5)
	userImpactOpenRatio        = 0.25
	userImpactHoldRatio        = 0.5
	userAlertRefreshInterval   = 5 * time.Minute
)

func userConds(now time.Time, sn *snapshot, signals store.HealthSignalBatch, activeKeys map[key]bool, d *derived, add func(cond)) {
	for _, user := range signals.Users {
		if user.Status == "expired" || user.Status == "limited" {
			alertKey := key{kAccessEnded, "", user.ID}
			if !user.AccessEndedAt.IsZero() && user.SubscriptionFetchAt.After(user.AccessEndedAt) &&
				now.Sub(user.AccessEndedAt) <= userAccessEndedMaxAge {
				variant := "expired"
				if user.Status == "limited" {
					variant = "quota"
				}
				switch {
				case signalWithin(user.SubscriptionFetchAt, now, userAccessEndedFetchWindow):
					add(cond{key: alertKey, severity: sevWarning,
						why:    "health.alert.access_ended.why." + variant,
						params: map[string]string{"user_name": user.Name, "since": strconv.FormatInt(user.AccessEndedAt.Unix(), 10)}})
				case activeKeys[alertKey] && signalWithin(user.SubscriptionFetchAt, now, userAccessEndedHoldWindow):
					d.holdKeys[alertKey] = true
				}
			}
		}

		if user.Status != "active" || user.LastTrafficAt.IsZero() || user.SubscriptionFetchAt.IsZero() {
			continue
		}
		fetchAge := now.Sub(user.SubscriptionFetchAt)
		trafficAge := now.Sub(user.LastTrafficAt)
		trafficBeforeFetch := user.SubscriptionFetchAt.Sub(user.LastTrafficAt)
		if fetchAge >= 0 && fetchAge <= userSilentFetchWindow &&
			trafficAge > userSilentTrafficMinAge && trafficAge <= userSilentTrafficMaxAge &&
			trafficBeforeFetch >= userSilentTrafficGap {
			subject := user.ID
			add(cond{key: key{kUserConnection, "", subject},
				severity: sevInfo, why: "health.alert.user_connection.why.silent",
				params: map[string]string{"user_name": user.Name}})
		}
	}

	for _, device := range signals.AWGDevices {
		if device.Status == "active" && device.LastHandshakeAt.IsZero() {
			age := now.Sub(device.CreatedAt)
			if age >= userNeverConnectedMinAge && age < userConnectionMaxAge {
				add(cond{key: key{kUserConnection, "", device.DeviceID}, severity: sevInfo,
					why:    "health.alert.user_connection.why.never_connected",
					params: map[string]string{"user_name": device.UserName}})
			}
		}
		// Only a key in recent use: a device abandoned for weeks says nothing about a broken connection.
		quiet := now.Sub(device.LastHandshakeAt)
		if device.Status == "active" && !device.LastHandshakeAt.IsZero() && quiet > userStaleHandshakeWindow && quiet < userConnectionMaxAge &&
			device.ConfigEpoch < device.CriticalEpoch {
			add(cond{key: key{kUserConnection, "", device.DeviceID}, severity: sevInfo,
				why:    "health.alert.user_connection.why.stale_key",
				params: map[string]string{"user_name": device.UserName}})
		}
	}

	addUsersImpacted(now, sn, signals.NodeHours, activeKeys, d, add)
}

func signalWithin(at, now time.Time, window time.Duration) bool {
	if at.IsZero() || at.After(now) {
		return false
	}
	return now.Sub(at) <= window
}

type nodeProtocol struct{ node, protocol string }

func addUsersImpacted(now time.Time, sn *snapshot, hours []store.HealthNodeHourSignal, activeKeys map[key]bool, d *derived, add func(cond)) {
	currentHour := now.UTC().Truncate(time.Hour).Unix()
	daySeconds := int64((24 * time.Hour) / time.Second)
	series := map[nodeProtocol][7]uint64{}
	current := map[nodeProtocol]uint64{}
	for alertKey := range activeKeys {
		if alertKey.kind == kUsersImpacted {
			pair := nodeProtocol{alertKey.node, alertKey.subject}
			if _, ok := series[pair]; !ok {
				series[pair] = [7]uint64{}
			}
		}
	}
	for _, hour := range hours {
		pair := nodeProtocol{hour.NodeID, hour.Protocol}
		if hour.Hour == currentHour {
			current[pair] = hour.PeakUsers
			if _, ok := series[pair]; !ok {
				series[pair] = [7]uint64{}
			}
			continue
		}
		daysAgo := (currentHour - hour.Hour) / daySeconds
		if daysAgo < 1 || daysAgo > 7 {
			continue
		}
		samples := series[pair]
		samples[daysAgo-1] = hour.PeakUsers
		series[pair] = samples
	}

	usuals := make(map[nodeProtocol]uint64, len(series))
	fleetUsual := map[string]uint64{}
	for pair, samples := range series {
		sort.Slice(samples[:], func(i, j int) bool { return samples[i] < samples[j] })
		usual := samples[len(samples)/2]
		usuals[pair] = usual
		fleetUsual[pair.protocol] += usual
	}
	fleetCurrent := map[string]uint64{}
	for pair, users := range current {
		fleetCurrent[pair.protocol] += users
	}

	lastConnectedAt := make(map[string]time.Time, len(sn.nodes))
	for _, node := range sn.nodes {
		lastConnectedAt[node.ID] = node.LastConnectedAt
	}

	for pair := range series {
		alertKey := key{kUsersImpacted, pair.node, pair.protocol}
		if !d.nodes[pair.node] || d.holdNode[pair.node] {
			continue
		}
		if !enabledProtocol(sn, pair.node, pair.protocol) {
			d.superseded[alertKey] = true
			continue
		}
		if explainedByCheck(pair.node, pair.protocol, sn, d, activeKeys) {
			d.superseded[alertKey] = true
			continue
		}

		active := activeKeys[alertKey]
		if now.Sub(lastConnectedAt[pair.node]) < userImpactNodeConnectGrace {
			if active {
				d.holdKeys[alertKey] = true
			}
			continue
		}
		if now.UTC().Sub(time.Unix(currentHour, 0)) < userImpactMinHourAge {
			if active {
				d.holdKeys[alertKey] = true
			}
			continue
		}

		usual := usuals[pair]
		if usual < userImpactMinUsual {
			if active {
				d.holdKeys[alertKey] = true
			}
			continue
		}
		fleetTypical := fleetUsual[pair.protocol]
		halfTypical := fleetTypical/2 + fleetTypical%2
		if fleetCurrent[pair.protocol] >= halfTypical {
			continue
		}
		nowUsers := current[pair]
		strong := float64(nowUsers) <= float64(usual)*userImpactOpenRatio
		weak := float64(nowUsers) <= float64(usual)*userImpactHoldRatio
		if !strong && !(active && weak) {
			continue
		}
		users := uint64(0)
		if usual > nowUsers {
			users = usual - nowUsers
		}
		add(cond{key: alertKey, severity: sevWarning, why: "health.alert.users_impacted.why.gone",
			params: map[string]string{"now": strconv.FormatUint(nowUsers, 10), "usual": strconv.FormatUint(usual, 10), "users": strconv.FormatUint(users, 10)}})
	}
}

func enabledProtocol(sn *snapshot, nodeID, protocol string) bool {
	for _, target := range sn.byNode[nodeID] {
		if target.in.Enabled && target.in.Protocol == protocol {
			return true
		}
	}
	return false
}

func explainedByCheck(nodeID, protocol string, sn *snapshot, d *derived, activeKeys map[key]bool) bool {
	noTrafficKey := key{kNoTraffic, nodeID, ""}
	if _, ok := d.conds[noTrafficKey]; ok || activeKeys[noTrafficKey] {
		return true
	}
	for _, target := range sn.byNode[nodeID] {
		if target.in.Protocol != protocol {
			continue
		}
		checkKey := key{kCheckFailed, nodeID, target.in.ID}
		if _, ok := d.conds[checkKey]; ok || activeKeys[checkKey] {
			return true
		}
	}
	return false
}

package doctor

// Detail codes: the stable name of the fact line of a Result ("<check id>.<variant>", agent.proto
// DoctorResult.detail_code). The admin UI writes a localized sentence per code from Result.Params (the English
// Detail stays for logs, old panels and codes the UI does not know). A code is never renamed; add new ones.
// Params named in the comments are what the sentence of that code uses; numbers are plain decimal strings.
const (
	// disk_space: mount, used_pct, free_mb, inode_pct
	CodeDiskUsage      = "disk_space.usage"
	CodeDiskUnreadable = "disk_space.unreadable"
	// journald_size: journal_mb, cap_mb
	CodeJournalSize = "journald_size.size"
	CodeJournalNone = "journald_size.none"
	// dstate_tasks: stuck, tasks
	CodeDstateNone      = "dstate_tasks.none"
	CodeDstateStuck     = "dstate_tasks.stuck"
	CodeDstateLoadIdle  = "dstate_tasks.load_idle"
	CodeDstateStuckLoad = "dstate_tasks.stuck_load_idle"
	CodeDstateNoProc    = "dstate_tasks.no_proc"
	CodeDstateCut       = "dstate_tasks.interrupted"
	// time_sync: offset_s, ntp_synced (yes | no | unknown)
	CodeTimeOffset = "time_sync.offset"
	// resolver: domains, failed_count, failed, median_ms
	CodeResolverOK     = "resolver.ok"
	CodeResolverFailed = "resolver.failed"
	// ipv6: ipv6 (yes | no), connect (ok | failed), warp_inbounds, client_ipv6 (off), suggest (client_ipv6_off);
	// with a global address also iface (interface of the main-table ::/0 route; never mg*, wg*, awg* or other tunnels),
	// addrs (its stable global addresses "addr/plen", comma-separated, at most 4) and src (the kernel's source address
	// for an outbound IPv6 connection); each is omitted when unknown. An agent that holds addresses for the panel adds
	// owned ("addr=state", comma-separated: ok | missing | tentative | dad_failed | denied); a DAD failure or a missing
	// or denied one warns (owned_dad_failed, owned_missing) unless the result already warns
	CodeIPv6None           = "ipv6.none"
	CodeIPv6NoneWarp       = "ipv6.none_warp"
	CodeIPv6OK             = "ipv6.ok"
	CodeIPv6Stalls         = "ipv6.connect_failed"
	CodeIPv6ClientOff      = "ipv6.client_off" // outbound IPv6 fails, but "IPv6 for clients" is off: clients use IPv4
	CodeIPv6OwnedMissing   = "ipv6.owned_missing"
	CodeIPv6OwnedDADFailed = "ipv6.owned_dad_failed"
	// foreign_vpn: names, count, on_our_ports
	CodeVPNNone  = "foreign_vpn.none"
	CodeVPNFound = "foreign_vpn.found"
	CodeVPNClash = "foreign_vpn.clash"
	// foreign_nft: table_count, rules, tables, nat_tables, ports
	CodeNftClean = "foreign_nft.clean"
	CodeNftFound = "foreign_nft.found"
	CodeNftNat   = "foreign_nft.nat"
	CodeNftHits  = "foreign_nft.hits_ports"
	CodeNftNone  = "foreign_nft.no_nft"
	CodeNftError = "foreign_nft.unreadable"
	// port_conflicts: checked, inbound_id, port, network, process, hop_from, hop_to, hop_holders
	CodePortsOK        = "port_conflicts.ok"
	CodePortsNone      = "port_conflicts.none"
	CodePortsHop       = "port_conflicts.hop"
	CodePortsTLS       = "port_conflicts.tls"
	CodePortsFailedBnd = "port_conflicts.bind_failed"
	CodePortsHeld      = "port_conflicts.held"
	CodePortsNoTable   = "port_conflicts.no_table"
	// net_baseline: differs, notes (comma separated tokens, see baselineDiff)
	CodeBaselineOK    = "net_baseline.ok"
	CodeBaselineNotes = "net_baseline.ok_notes"
	CodeBaselineDiff  = "net_baseline.differs"
	// cert_expiry: checked, reason, days_left, inbound_id, server_name
	CodeCertOK         = "cert_expiry.ok"
	CodeCertAgent      = "cert_expiry.agent"
	CodeCertInbound    = "cert_expiry.inbound"
	CodeCertUnreadable = "cert_expiry.unreadable"
	CodeCertNone       = "cert_expiry.none"
	// memory_pressure: avail_pct, swap_pct, oom_kills (a number or "unknown")
	CodeMemUsage  = "memory_pressure.usage"
	CodeMemNoInfo = "memory_pressure.no_meminfo"
	// cpu_softirq: softirq_pct, cpu_pct, samples, need
	CodeCPULoad       = "cpu_softirq.load"
	CodeCPUCollecting = "cpu_softirq.collecting"
	// kernel_headers: kernel, missing, mode (auto | kernel | userspace), virt
	CodeHeadersNoAwg     = "kernel_headers.no_awg"
	CodeHeadersContainer = "kernel_headers.container"
	CodeHeadersReady     = "kernel_headers.ready"
	CodeHeadersUserspace = "kernel_headers.userspace"
	CodeHeadersOptional  = "kernel_headers.optional"
	CodeHeadersMissing   = "kernel_headers.missing"
	// awg_backend: backend, version, mode, reason, hint
	CodeAwgNone        = "awg_backend.none"
	CodeAwgNoEngine    = "awg_backend.no_engine"
	CodeAwgRunning     = "awg_backend.running"
	CodeAwgForwardDrop = "awg_backend.forward_drop"
	CodeAwgUnavailable = "awg_backend.unavailable"
	// warp_path: state, backend, colo, inbounds, hint, error
	CodeWarpNoManager   = "warp_path.no_manager"
	CodeWarpUnused      = "warp_path.unused"
	CodeWarpNoAccount   = "warp_path.not_configured"
	CodeWarpHostClash   = "warp_path.host_clash"
	CodeWarpUp          = "warp_path.up"
	CodeWarpCheckFailed = "warp_path.check_failed"
	CodeWarpStarting    = "warp_path.starting"
	CodeWarpNoBackend   = "warp_path.no_backend"
	CodeWarpPausedUsed  = "warp_path.paused_used"
	CodeWarpPaused      = "warp_path.paused"
	CodeWarpDown        = "warp_path.down"
	CodeWarpUnknown     = "warp_path.unknown_state"
	// unit_generation: unit_gen, current, reason, from_gen, to_gen
	CodeUnitCurrent       = "unit_generation.current"
	CodeUnitOutdated      = "unit_generation.outdated"
	CodeUnitManual        = "unit_generation.not_systemd"
	CodeUnitOtherUnit     = "unit_generation.other_unit"
	CodeUnitUnknown       = "unit_generation.unknown"
	CodeUnitRefreshFailed = "unit_generation.refresh_failed"
	// security_updates: off (raw settings and timer states)
	CodeSecurityUpdatesOn           = "security_updates.on"
	CodeSecurityUpdatesNotInstalled = "security_updates.not_installed"
	CodeSecurityUpdatesOff          = "security_updates.off"
	CodeSecurityUpdatesNotAPT       = "security_updates.not_apt"
	CodeSecurityUpdatesNoSystemd    = "security_updates.no_systemd"
	CodeSecurityUpdatesEnabling     = "security_updates.enabling"
	CodeSecurityUpdatesEnableFailed = "security_updates.enable_failed"
	// Any check: why it did not run. skip.unsupported has reason (English, from the host probe).
	CodeSkipUnsupported = "skip.unsupported"
	CodeSkipTimeout     = "skip.timeout"
	CodeSkipInternal    = "skip.internal"
	CodeSkipUnknown     = "skip.unknown_check"
)

// Codes lists every detail code, for the test that keeps the admin UI's wording in step.
var Codes = []string{
	CodeDiskUsage, CodeDiskUnreadable, CodeJournalSize, CodeJournalNone,
	CodeDstateNone, CodeDstateStuck, CodeDstateLoadIdle, CodeDstateStuckLoad, CodeDstateNoProc, CodeDstateCut,
	CodeTimeOffset, CodeResolverOK, CodeResolverFailed,
	CodeIPv6None, CodeIPv6NoneWarp, CodeIPv6OK, CodeIPv6Stalls, CodeIPv6ClientOff, CodeIPv6OwnedMissing, CodeIPv6OwnedDADFailed,
	CodeVPNNone, CodeVPNFound, CodeVPNClash,
	CodeNftClean, CodeNftFound, CodeNftNat, CodeNftHits, CodeNftNone, CodeNftError,
	CodePortsOK, CodePortsNone, CodePortsHop, CodePortsTLS, CodePortsFailedBnd, CodePortsHeld, CodePortsNoTable,
	CodeBaselineOK, CodeBaselineNotes, CodeBaselineDiff,
	CodeCertOK, CodeCertAgent, CodeCertInbound, CodeCertUnreadable, CodeCertNone,
	CodeMemUsage, CodeMemNoInfo, CodeCPULoad, CodeCPUCollecting,
	CodeHeadersNoAwg, CodeHeadersContainer, CodeHeadersReady, CodeHeadersUserspace, CodeHeadersOptional, CodeHeadersMissing,
	CodeAwgNone, CodeAwgNoEngine, CodeAwgRunning, CodeAwgForwardDrop, CodeAwgUnavailable,
	CodeWarpNoManager, CodeWarpUnused, CodeWarpNoAccount, CodeWarpHostClash, CodeWarpUp, CodeWarpCheckFailed, CodeWarpStarting,
	CodeWarpNoBackend, CodeWarpPausedUsed, CodeWarpPaused, CodeWarpDown, CodeWarpUnknown,
	CodeUnitCurrent, CodeUnitOutdated, CodeUnitManual, CodeUnitOtherUnit, CodeUnitUnknown, CodeUnitRefreshFailed,
	CodeSecurityUpdatesOn, CodeSecurityUpdatesNotInstalled, CodeSecurityUpdatesOff, CodeSecurityUpdatesNotAPT, CodeSecurityUpdatesNoSystemd,
	CodeSecurityUpdatesEnabling, CodeSecurityUpdatesEnableFailed,
	CodeSkipUnsupported, CodeSkipTimeout, CodeSkipInternal, CodeSkipUnknown,
}

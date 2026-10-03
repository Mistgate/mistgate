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
	// ipv6: ipv6 (yes | no), connect (ok | failed), warp_inbounds
	CodeIPv6None     = "ipv6.none"
	CodeIPv6NoneWarp = "ipv6.none_warp"
	CodeIPv6OK       = "ipv6.ok"
	CodeIPv6Stalls   = "ipv6.connect_failed"
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
	CodeIPv6None, CodeIPv6NoneWarp, CodeIPv6OK, CodeIPv6Stalls,
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
	CodeSkipUnsupported, CodeSkipTimeout, CodeSkipInternal, CodeSkipUnknown,
}

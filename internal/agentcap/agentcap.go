// Package agentcap holds the capability strings an agent lists in Hello.capabilities and a panel asks for (agent.proto). A string
// is a wire value: an agent in the field lists the one it was built with and a panel compares it byte for byte, so a value never
// changes and a feature that needs a new behavior gets a new string. The package imports nothing: the agent, the panel and the
// edge builds all read it, and testdata/caps.golden keeps every value from being renamed.
package agentcap

// The L3 and engine strings (agent.proto "AWG AND WARP"). The panel puts an inbound or a WarpSpec into the desired state of a
// stream only when the stream listed the string its protocol needs; an older agent would reject what it does not know, and the
// whole state with it.
const (
	AWG = "awg/1"
	// AWGFallback: a "kernel" setting runs on userspace when the module does not load, the doctor says awg_backend.fallback and the
	// fix awg_module_repair moves back. An agent without it stays strict: the panel offers its owner "switch to auto".
	AWGFallback = "awg-fallback/1"
	// AWGPrepare: the agent builds the AmneziaWG kernel module on request (PrepareAwgKernel).
	AWGPrepare = "awg-prepare/1"
	VLESS      = "vless/1"
	// VLESSUnknownKeys marks agents that report VLESS unknown-user-ID attempts in links/1.
	VLESSUnknownKeys = "vless-unknown-keys/1"
	// VLESSHelloKex marks agents that count the key exchange the ClientHellos of REALITY connections offer, per key
	// (agent.proto LinkCounters hello_pq, hello_classic, hello_unknown).
	VLESSHelloKex = "vless-hello-kex/1"
	// VLESSTLS: the VLESS engine terminates TLS through its certificate source before Xray receives a connection.
	VLESSTLS = "vless-tls/1"
	// VLESSSelf: the VLESS engine serves the node's own site behind REALITY (reality.target "self"). An agent of v0.1.33/v0.1.34
	// has vless/1 and rejects that target, and self is the default of a new VLESS profile.
	VLESSSelf = "vless-self/1"
	// VLESSTarget: the VLESS engine takes a REALITY target set on this node: the other site's name in the TLS block (mode 0, no
	// certificate) as the one server name. An agent of vless-self/1 rejects that TLS block, so the panel withholds such an inbound.
	VLESSTarget = "vless-target/1"
	// TrustTunnel marks the in-process TCP HTTP/2 TrustTunnel engine.
	TrustTunnel = "trusttunnel/1"
	// TrustTunnelH3: the engine also serves TrustTunnel over HTTP/3 on UDP at the TCP listener's port. An agent that lists only
	// trusttunnel/1 rejects h3:true, so such an inbound is withheld from it.
	TrustTunnelH3 = "trusttunnel-h3/1"
	// TrustTunnelRefusals marks the engine that counts the tunnel requests it refused, by reason, on the inbound's row
	// (agent.proto LinkCounters tunnel_*).
	TrustTunnelRefusals = "trusttunnel-refusals/1"
	// TrustTunnelTCPStalls marks the engine that counts the HTTP/2 connections that ended with data the client never
	// acknowledged (agent.proto LinkCounters tcp_stalls, tcp_stalls_16k), on the inbound's row.
	TrustTunnelTCPStalls = "trusttunnel-tcp-stalls/1"
	// MTProto: the agent has the MTProto engine. No agent lists it: the panel derives it from Hello.engines, because the agents of
	// v0.1.69 have the engine without the string, and v0.1.68 rejects the whole state for an mtproto inbound.
	MTProto = "mtproto/1"
	// Warp: the agent can run WARP; the panel registers or imports an account only for a node that listed it.
	Warp = "warp/1"
	// WarpReach: the agent answers WarpReach (agent.proto "WARP REACH").
	WarpReach = "warp-reach/1"
	// UserRateLimit: the engines that shape traffic per credential also share a bucket per person (Credential.user_rate_limit_bps,
	// agent.proto "SPEED LIMITS"). An agent without it is sent the person limit folded into the device limit.
	UserRateLimit = "user-rate-limit/1"
	// TCPShare: the agent shares a TCP port between services by SNI (agent.proto "SHARED TCP PORT") and reports
	// StatsBatch.shared_ports. An agent without it holds one service of a port and fails the rest on bind, so it is sent only the
	// default route of such a port.
	TCPShare = "tcp-share/1"
	// RelayForward: the agent can forward ports (DesiredState.relays, ApplyResult.relays); the panel sends relays only to a stream
	// that listed it. RelayReverse: it can run the bridge of a reverse relay (RelayForward.mode, DesiredState.bridge_serve and
	// bridge_dials, ApplyResult.bridges, StatsBatch.bridges; agent.proto "REVERSE RELAY"); a pair of agents needs it at both ends.
	RelayForward = "relay-forward/1"
	RelayReverse = "relay-reverse/1"
)

// The reports and requests of a node: counters, probes, checks and captures.
const (
	// Doctor: the panel sends RunDoctor and ApplyFix only to an agent that listed it (agent.proto "DOCTOR").
	Doctor = "doctor/1"
	// Bandwidth: the panel sends MeasureBandwidth only to an agent that listed it.
	Bandwidth = "bandwidth/1"
	// ClientIPv6: the agent follows NodeSettings.client_ipv6_disabled (the tunnel firewall and the direct egress).
	ClientIPv6 = "client-ipv6/1"
	// IPv6Addr: the agent follows NodeSettings.ipv6_addresses.
	IPv6Addr = "ipv6-addr/1"
	WSLink   = "ws-link/1"
	Decoy    = "decoy/1"
	// Links is the connection counters of StatsBatch.links. Links2 adds the Hysteria2 connection endings, RTT and packet counters
	// (LinkCounters from client_closes on); it is listed with links/1, which a panel checks for the counters at all. Links3 adds the
	// handshake-failure reasons (LinkCounters from hs_unfinished on) and the VLESS hellos, hellos_foreign and connects.
	Links  = "links/1"
	Links2 = "links/2"
	Links3 = "links/3"
	// Nets enables locally mapped ASN counters. Link-only agents cannot fetch the signed table in v1.
	Nets = "nets/1"
	// Journal sends the node's deduplicated WARN+ records in StatsBatch.journal.
	Journal      = "journal/1"
	TorrentGuard = "torrentguard/1"
	UDPCheck     = "udpcheck/1"
	TCPCheck     = "tcpcheck/1"
	// CountryProbe: this host can run CountryProbe (agent.proto "COUNTRY PROBE").
	CountryProbe = "country-probe/1"
	// Probe: UdpSend.df, the "connect" kind and the timings of TcpProbe, PathTrace (agent.proto "PATH PROBE").
	Probe = "probe/1"
	// Speed: SpeedTest (agent.proto "PATH SPEED").
	Speed = "speed/1"
	// SpeedChannel: the 8-stream timed mode of SpeedTest (steps 4 and 5 of its size; agent.proto "CHANNEL MEASURE").
	SpeedChannel = "speed-channel/1"
	// Debug: the agent captures its debug log on request (agent.proto "DEBUG CAPTURE"), and the panel sends DebugCapture only to an
	// agent that listed it; Debug2 adds the kernel source.
	Debug  = "debug/1"
	Debug2 = "debug/2"
	// SNIScan: the agent answers SniScan (agent.proto "NEIGHBOR SITES").
	SNIScan = "sni-scan/1"
)

// The agent's own unit and the updates of the node and its system.
const (
	// Unit3 says the node runs from a systemd unit of generation 3: /dev/net/tun is reachable (the userspace AWG and WARP backends
	// need it) and ExecStopPost cleans the tunnel interfaces. A node without it keeps working with the kernel backends and shows
	// the hint "unit_outdated" on the awg_backend doctor check.
	Unit3 = "unit/3"
	// UnitRefresh: the agent can refresh its systemd unit (the doctor fix refresh_unit).
	UnitRefresh = "unit-refresh/1"
	// Update (agent.proto "UPDATE") is listed only when the process can really update itself: signed build, Linux and a writable
	// executable directory (false under the generation 1 unit, which is what keeps old nodes on "update by hand once").
	Update        = "update/1"
	UpdateTimeout = "update-timeout/1"
	// UpdateSources gates UpdateAgent.sources and UpdateProgress.source.
	UpdateSources = "update-sources/1"
	// UpdateGuard: the unit carries the crash-loop guard.
	UpdateGuard = "update-guard/1"
	// UpdateLink is the update capability of an edge link-only agent; a VPS agent is asked for update/1.
	UpdateLink = "update-link/1"
	// OSStatus advertises the read-only OS snapshot stream. It is deliberately not os-update/1: an agent that reads the system can
	// be unable to run a job. OSUpdate: the agent runs OS jobs on request (agent.proto "OS UPDATES").
	OSStatus = "os-status/1"
	OSUpdate = "os-update/1"
)

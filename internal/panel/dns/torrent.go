package dns

// Torrent traffic direct (Preset.TorrentDirect). The apps that can tell a torrent client by its program, or the traffic by its protocol,
// send it past the VPN: a client's peers are many and ever-changing, and a torrent swarm over a tunnel is slow and costs the server's
// traffic. What each format can do (docs/en/guide/dns.md):
//
//   - Mihomo: PROCESS-NAME rules for the programs of torrentProcesses and the Android packages of torrentPackages, and the rule set of
//     the public trackers (category-public-tracker), all to DIRECT.
//   - sing-box: process_name and package_name, protocol bittorrent (the profile sniffs) and the tracker rule set, all to the direct
//     outbound. process_name works on desktop systems, package_name on Android.
//   - Xray JSON: protocol bittorrent (the config sniffs, so only what the sniffer recognises) and geosite:category-public-tracker.
//     No program names: Xray cannot tell the program.
//   - Happ, AmneziaWG keys and the plain link list: nothing, they have no such rules.
//
// The rules stand after the blocks (ads, Max) and before the split. A person's real address is seen in a swarm, and a tracker blocked in
// the country does not open direct: that is why the switch is off by default and says so.
//
// The lists of programs are the panel's own, the usual torrent clients by the name their process has on Windows, macOS and Linux and by
// their Android package; a client that is not in them is not sent direct by name, only by the protocol where the format has one.
const (
	TorrentSite = "category-public-tracker" // geosite list of public trackers (v2fly/domain-list-community data file)
	torrentTag  = "geosite-" + TorrentSite  // its rule set in Mihomo and sing-box
)

var torrentProcesses = []string{
	"qbittorrent.exe", "qbittorrent", "qBittorrent",
	"utorrent.exe", "uTorrent", "utserver",
	"bittorrent.exe", "BitTorrent",
	"transmission-qt.exe", "transmission-qt", "transmission-gtk", "transmission-daemon", "Transmission",
	"deluge.exe", "deluge", "deluge-gtk", "deluged",
	"tixati.exe", "tixati", "Tixati",
	"bitcomet.exe", "BitComet",
	"vuze.exe", "Vuze", "azureus",
	"biglybt.exe", "BiglyBT", "biglybt",
	"frostwire.exe", "FrostWire", "frostwire",
	"rtorrent",
}

var torrentPackages = []string{
	"org.proninyaroslav.libretorrent", // LibreTorrent
	"com.bittorrent.client",           // BitTorrent
	"com.utorrent.client",             // µTorrent
	"com.utorrent.client.pro",
	"com.delphicoder.flud", // Flud
	"com.delphicoder.flud.paid",
	"com.biglybt.android.client", // BiglyBT
	"com.frostwire.android",      // FrostWire
}

// torrentApps are the names a program rule takes, in one list: Mihomo's PROCESS-NAME holds an Android package as well.
func torrentApps() []string {
	return append(append([]string{}, torrentProcesses...), torrentPackages...)
}

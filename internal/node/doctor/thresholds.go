package doctor

import (
	"time"

	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// Every threshold of the checks in one place, so they can be tuned without reading the checks.
// Run budgets: every check gets checkBudget, the whole run runBudget. Variables only so a test can shorten them.
var (
	checkBudget = 10 * time.Second
	runBudget   = 30 * time.Second
)

const (
	// disk_space
	diskWarnUsedPct  = 80
	diskFailUsedPct  = 92
	diskWarnFree     = 1 << 30   // bytes
	diskFailFree     = 300 << 20 // bytes
	diskFailInodePct = 95
	// A fix is offered when the journal is at least this big (vacuuming a small one frees nothing).
	diskVacuumMin = 300 << 20

	// journald_size: the cap baseline installs is 200 MiB, so above 300 MiB the cap is not effective.
	journalWarn = 300 << 20
	journalFail = 1 << 30
	journalCap  = hostctl.JournalCapMB << 20

	// dstate_tasks: a task must sit in D in every sample to count as stuck.
	dstateSamples   = 3
	dstateGap       = 2 * time.Second
	dstateLoadFrac  = 0.9 // load1 >= this x CPUs ...
	dstateIdleCPU   = 15  // ... while total CPU is below this %
	dstateLoadSpan  = 10 * time.Minute
	dstateLoadMin   = 30 // samples needed before the load rule can fire
	dstateMaxListed = 5

	// time_sync
	clockWarnS = 2
	clockFailS = 30

	// resolver
	resolverTimeout = 3 * time.Second
	resolverSlow    = 500 * time.Millisecond

	// ipv6
	ipv6Timeout = 3 * time.Second

	// foreign_vpn / foreign_nft list at most this many names.
	maxNames = 8

	// cert_expiry
	certWarn      = 14 * 24 * time.Hour // ACME renews at 30 days, so this means renewal is failing
	certFail      = 3 * 24 * time.Hour
	agentCertWarn = 5 * 24 * time.Hour
	agentCertFail = 24 * time.Hour
	certDialTime  = 3 * time.Second

	// memory_pressure
	memWarnAvailPct = 12
	memFailAvailPct = 5
	swapWarnPct     = 50
	psiWarnSome     = 10.0 // some avg60
	psiFailFull     = 5.0  // full avg60

	// cpu_softirq
	softirqWarn       = 50.0
	softirqFail       = 90.0
	cpuFail           = 97.0
	softirqWindow     = 10 * time.Minute
	softirqMinSamples = 30
)

//go:build !js

package ops

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/sysstat"
)

// The panel's own cost (GetPanelResources): its process, the machine, its database and the admin API's load. Reads only:
// /proc of this process and machine, statfs of the data directory, SQLite's PRAGMAs (the VPS edition's database; ADR 0002
// keeps PRAGMA out of D1, and this file is not built for it) and the call counter of the HTTP middleware.

// processStart is when this process loaded the package: its uptime counts from here.
var processStart = time.Now()

const (
	// A CPU share needs two reads. The first call after a pause reads twice resPrime apart; later calls read the time since the
	// previous call, and calls closer than resMinGap get the previous answer.
	resPrime  = 500 * time.Millisecond
	resStale  = 2 * time.Minute
	resMinGap = time.Second
	// The database is measured at most once a minute: page counts and two stats of files.
	dbEvery = time.Minute
)

type resourcesState struct {
	mu   sync.Mutex // the samplers and their last reading
	host sysstat.HostSampler
	proc sysstat.ProcSampler
	at   time.Time
	h    sysstat.Host
	p    sysstat.Process

	dbMu sync.Mutex
	db   dbStat
	dbAt time.Time
}

type dbStat struct {
	path                         string
	file, wal, pages, free, size uint64
}

func (r rpc) GetPanelResources(ctx context.Context, _ *connect.Request[opsv1.GetPanelResourcesRequest]) (*connect.Response[opsv1.GetPanelResourcesResponse], error) {
	s := r.s
	h, p := s.sampleSelf(ctx)
	g := sysstat.ReadGoRuntime()
	db, dbAt := s.dbStats(ctx)
	u32 := func(v uint64) uint32 { return uint32(min(v, math.MaxUint32)) }
	now := s.cfg.Now()
	resp := &opsv1.GetPanelResourcesResponse{
		NowUnix: now.Unix(),
		Process: &opsv1.PanelProcess{
			CpuPct: float32(p.CPUPct), RssBytes: p.RSS, GoTotalBytes: g.TotalBytes, GoHeapBytes: g.HeapBytes,
			Goroutines: u32(g.Goroutines), Threads: p.Threads, OpenFds: u32(p.FDs), FdLimit: u32(p.FDLimit),
			UptimeS: uint64(max(time.Since(processStart), 0) / time.Second),
		},
		Host: &opsv1.PanelHost{
			Cpus: u32(uint64(runtime.NumCPU())), CpuBusyPct: float32(h.CPU.Busy), StealPct: float32(h.CPU.Steal), IowaitPct: float32(h.CPU.IOWait),
			Load1: float32(h.Load1), RamUsedBytes: h.Mem.Used(), RamTotalBytes: h.Mem.Total, SwapUsedBytes: h.Mem.SwapUsed(), SwapTotalBytes: h.Mem.SwapTotal,
		},
		Database: &opsv1.PanelDatabase{FileBytes: db.file, WalBytes: db.wal, PageCount: db.pages, FreelistCount: db.free, PageSize: u32(db.size)},
	}
	if !dbAt.IsZero() {
		resp.Database.MeasuredUnix = dbAt.Unix()
	}
	if db.path != "" {
		resp.Host.DiskTotalBytes, resp.Host.DiskUsedBytes = sysstat.DiskUsage(filepath.Dir(db.path))
	}
	if rows, err := s.cfg.Store.NodeLive(ctx, now.UTC(), "", false); err == nil {
		for _, l := range rows {
			if l.State == "retired" {
				continue
			}
			resp.Nodes++
			if l.Connected {
				resp.AgentsConnected++
			}
		}
	} else {
		s.cfg.Log.Warn("ops: count agents", "code", "ops_failed", "err", err)
	}
	rs := s.cfg.RPC.Snapshot()
	resp.Rpc = &opsv1.PanelRpcLoad{WindowS: u32(uint64(rs.WindowS)), Requests: rs.Requests, ServerErrors: rs.ServerErrors, ClientErrors: rs.ClientErrors,
		P95Ms: float32(rs.P95ms)}
	return connect.NewResponse(resp), nil
}

// sampleSelf reads the machine and this process, as shares of the time since the previous reading.
func (s *Service) sampleSelf(ctx context.Context) (sysstat.Host, sysstat.Process) {
	st := &s.res
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	if !st.at.IsZero() && now.Sub(st.at) < resMinGap {
		return st.h, st.p
	}
	if st.at.IsZero() || now.Sub(st.at) > resStale {
		st.host.Sample()
		st.proc.Sample()
		t := time.NewTimer(resPrime)
		select {
		case <-ctx.Done():
		case <-t.C:
		}
		t.Stop()
	}
	st.h, st.p, st.at = st.host.Sample(), st.proc.Sample(), time.Now()
	return st.h, st.p
}

// dbStats measures the database, at most once every dbEvery. A failed read is retried at the next call.
func (s *Service) dbStats(ctx context.Context) (dbStat, time.Time) {
	st := &s.res
	st.dbMu.Lock()
	defer st.dbMu.Unlock()
	now := time.Now()
	if !st.dbAt.IsZero() && now.Sub(st.dbAt) < dbEvery {
		return st.db, st.dbAt
	}
	d, err := readDBStat(ctx, s)
	if err != nil {
		s.cfg.Log.Warn("ops: measure the database", "code", "ops_failed", "err", err)
		return st.db, st.dbAt
	}
	st.db, st.dbAt = d, now
	return d, now
}

func readDBStat(ctx context.Context, s *Service) (dbStat, error) {
	var d dbStat
	db := s.cfg.Store.R
	var pages, free, size int64
	if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return d, err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
		return d, err
	}
	if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return d, err
	}
	d.pages, d.free, d.size = uint64(max(pages, 0)), uint64(max(free, 0)), uint64(max(size, 0))
	rows, err := db.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var seq int64
		var name, file string
		if err := rows.Scan(&seq, &name, &file); err != nil {
			return d, err
		}
		if name == "main" {
			d.path = file
		}
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	if d.path != "" {
		if fi, err := os.Stat(d.path); err == nil {
			d.file = uint64(fi.Size())
		}
		if fi, err := os.Stat(d.path + "-wal"); err == nil {
			d.wal = uint64(fi.Size())
		}
	}
	if d.file == 0 { // an in-memory database, or a file that cannot be read: the pages say its size
		d.file = d.pages * d.size
	}
	return d, nil
}

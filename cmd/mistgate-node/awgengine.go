package main

import (
	"context"
	"log/slog"
	"sync"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// awgEngine is the AmneziaWG engine with a late backend choice. The backend (kernel module or userspace) is chosen
// from NodeSettings.awg_backend, which the panel sends after the agent was built (HelloAck, DesiredState) and may change
// at any time; awg.Engine chooses once at construction. So the real engine is built on first use with the setting of
// that moment, and a changed setting closes it (its interfaces go) and asks the agent, through agent.SettingsAware, to
// apply every awg inbound again, which builds a new engine with the new choice. Detection only probes (a genetlink
// lookup, opening /dev/net/tun); nothing is installed or loaded here, `awg prepare-kernel` is the explicit step for that.
//
// Only Apply, Version and BackendStatus build the engine. The readers the agent calls on every statistics batch
// (Collect, Observed, Health, AwgHealth) and Kick, Remove and Close find nothing to do while it was never built: a node
// that serves no AWG never probes for a backend because of them.
type awgEngine struct {
	env  engine.Env
	mode func() string // NodeSettings.awg_backend as the agent holds it ("" = auto)

	mu    sync.Mutex
	eng   *awg.Engine
	built string // the mode eng was built with
}

func newAwgEngine(env engine.Env, mode func() string) *awgEngine {
	if env.Log == nil {
		env.Log = slog.Default()
	}
	return &awgEngine{env: env, mode: mode}
}

// normMode maps the setting to one of the three modes; anything else is "auto" (a panel from the future must not
// keep an agent from serving).
func normMode(s string) string {
	switch s {
	case "kernel", "userspace":
		return s
	}
	return "auto"
}

// get returns the engine, building it if needed.
func (l *awgEngine) get() *awg.Engine {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.eng == nil {
		m := normMode(l.mode())
		e, err := awg.NewWithOptions(l.env, awg.Options{Backend: m})
		if err != nil { // only a bad mode, which normMode excludes; keep the agent alive anyway
			l.env.Log.Error("awg engine", "err", err)
			e, _ = awg.NewWithOptions(l.env, awg.Options{Backend: "auto"})
			m = "auto"
		}
		l.eng, l.built = e, m
	}
	return l.eng
}

// peek returns the engine if it was built, else nil.
func (l *awgEngine) peek() *awg.Engine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.eng
}

// NodeSettings implements agent.SettingsAware.
func (l *awgEngine) NodeSettings(ctx context.Context, st *pb.NodeSettings) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.eng == nil || l.built == normMode(st.AwgBackend) {
		return false
	}
	l.env.Log.Info("awg backend setting changed, the engine is rebuilt", "from", l.built, "to", normMode(st.AwgBackend))
	if err := l.eng.Close(ctx); err != nil {
		l.env.Log.Warn("awg engine close", "err", err)
	}
	l.eng, l.built = nil, ""
	return true
}

// BackendStatus is the doctor's view (the agent asserts this method).
func (l *awgEngine) BackendStatus() awg.BackendStatus { return l.get().BackendStatus() }

// AwgHealth implements awg.HealthReporter.
func (l *awgEngine) AwgHealth() []awg.InboundHealth {
	if e := l.peek(); e != nil {
		return e.AwgHealth()
	}
	return nil
}

func (l *awgEngine) Protocol() string { return awg.Protocol }

// Version is EngineInfo.version in Hello. The agent asks for it on every connect, BEFORE the panel's NodeSettings arrive and
// whether or not the node serves AWG, so it must not build the engine (that probes for a backend and would fix the choice with
// a setting that is about to change): until the first awg inbound builds it, it only says what it is; after that it names the
// backend, and the next Hello carries that text.
func (l *awgEngine) Version() string {
	if e := l.peek(); e != nil {
		return e.Version()
	}
	return "amneziawg (backend chosen at the first inbound)"
}

// Capabilities: the interface is shared by all users of a profile, so no per-credential limit, and a credential cannot
// expire by itself on the device (the agent withdraws it); awg.Engine says the same.
func (l *awgEngine) Capabilities() engine.Capabilities { return engine.Capabilities{} }

func (l *awgEngine) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	return l.get().Apply(ctx, spec, creds)
}

func (l *awgEngine) Remove(ctx context.Context, id string) error {
	if e := l.peek(); e != nil {
		return e.Remove(ctx, id)
	}
	return nil
}

func (l *awgEngine) Collect(ctx context.Context) (engine.Collected, error) {
	if e := l.peek(); e != nil {
		return e.Collect(ctx)
	}
	return engine.Collected{}, nil
}

func (l *awgEngine) Kick(ctx context.Context, ids []string) (int, error) {
	if e := l.peek(); e != nil {
		return e.Kick(ctx, ids)
	}
	return 0, nil
}

func (l *awgEngine) Observed() []statehash.Inbound {
	if e := l.peek(); e != nil {
		return e.Observed()
	}
	return nil
}

func (l *awgEngine) Health() []plugin.EngineHealth {
	if e := l.peek(); e != nil {
		return e.Health()
	}
	return nil
}

// Close closes the real engine if one was built.
func (l *awgEngine) Close(ctx context.Context) error {
	l.mu.Lock()
	e := l.eng
	l.eng, l.built = nil, ""
	l.mu.Unlock()
	if e == nil {
		return nil
	}
	return e.Close(ctx)
}

var (
	_ engine.Engine      = (*awgEngine)(nil)
	_ awg.HealthReporter = (*awgEngine)(nil)
)

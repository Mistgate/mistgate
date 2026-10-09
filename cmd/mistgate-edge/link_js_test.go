//go:build js && wasm

package main

import (
	"context"
	"errors"
	"math"
	"syscall/js"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/statehash"
	"google.golang.org/protobuf/proto"
)

func edgeInitOptions(nodeLink js.Value, includeNodeLink bool) js.Value {
	options := js.Global().Get("Object").New()
	options.Set("d1", js.Global().Get("Object").New())
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	options.Set("masterKey", bytesToJS(key))
	options.Set("publicURL", "https://example.com")
	options.Set("adminPrefix", "/test-admin/")
	if includeNodeLink {
		options.Set("nodeLink", nodeLink)
	}
	return options
}

func hasJSProperty(value js.Value, name string) bool {
	return js.Global().Get("Reflect").Call("has", value, name).Bool()
}

func makeStepArgs(nodeID, state string, stateIsNull bool, until int64, event js.Value) js.Value {
	in := js.Global().Get("Object").New()
	in.Set("nodeId", nodeID)
	if stateIsNull {
		in.Set("state", js.Null())
	} else {
		in.Set("state", state)
	}
	in.Set("until", until)
	in.Set("event", event)
	return in
}

func makeStepEvent(kind string, at int64) js.Value {
	event := js.Global().Get("Object").New()
	event.Set("kind", kind)
	event.Set("at", at)
	return event
}

func TestParseInitOptionsNodeLink(t *testing.T) {
	ask := js.FuncOf(func(js.Value, []js.Value) any { return nil })
	defer ask.Release()
	closeNode := js.FuncOf(func(js.Value, []js.Value) any { return nil })
	defer closeNode.Release()
	poke := js.FuncOf(func(js.Value, []js.Value) any { return 0 })
	defer poke.Release()

	without := edgeInitOptions(js.Undefined(), false)
	parsed, err := parseInitOptions(without)
	if err != nil || parsed.hasNodeLink {
		t.Fatalf("nodeLink omitted: options=%+v err=%v", parsed, err)
	}

	valid := js.Global().Get("Object").New()
	valid.Set("ask", ask)
	valid.Set("close", closeNode)
	valid.Set("poke", poke)
	parsed, err = parseInitOptions(edgeInitOptions(valid, true))
	if err != nil || !parsed.hasNodeLink || parsed.nodeLink.Get("ask").Type() != js.TypeFunction ||
		parsed.nodeLink.Get("close").Type() != js.TypeFunction || parsed.nodeLink.Get("poke").Type() != js.TypeFunction {
		t.Fatalf("nodeLink functions: parsed=%+v err=%v", parsed, err)
	}

	invalid := []struct {
		name  string
		value js.Value
	}{
		{name: "null", value: js.Null()},
		{name: "string", value: js.ValueOf("invalid")},
		{name: "missing ask", value: js.Global().Get("Object").New()},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "missing ask" {
				tc.value.Set("close", closeNode)
			}
			if _, err := parseInitOptions(edgeInitOptions(tc.value, true)); !errors.Is(err, errInvalidNodeLink) {
				t.Fatalf("parseInitOptions() error = %v, want %v", err, errInvalidNodeLink)
			}
		})
	}
	missingClose := js.Global().Get("Object").New()
	missingClose.Set("ask", ask)
	if _, err := parseInitOptions(edgeInitOptions(missingClose, true)); !errors.Is(err, errInvalidNodeLink) {
		t.Fatalf("missing close error = %v, want %v", err, errInvalidNodeLink)
	}
	missingPoke := js.Global().Get("Object").New()
	missingPoke.Set("ask", ask)
	missingPoke.Set("close", closeNode)
	if _, err := parseInitOptions(edgeInitOptions(missingPoke, true)); !errors.Is(err, errInvalidNodeLink) {
		t.Fatalf("missing poke error = %v, want %v", err, errInvalidNodeLink)
	}
}

func TestNewAndEnsureEdgeLinkPrefix(t *testing.T) {
	in, pending, err := newEdgeInstance(initOptions{publicURL: "https://example.com", adminPath: "/test-admin/", subPath: "/test-sub/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.LinkPrefix) < 4 || in.LinkPrefix[0] != '/' || in.LinkPrefix[len(in.LinkPrefix)-1] != '/' || pending["link_prefix"] != in.LinkPrefix {
		t.Fatalf("new link prefix = %q, pending %q", in.LinkPrefix, pending["link_prefix"])
	}

	ctx := context.Background()
	st, err := store.OpenD1(ctx, js.Global().Get("__d1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	instance := app.InstanceConfig{PublicURL: "https://example.com"}
	fill, err := ensureEdgeSecrets(ctx, st, &instance)
	if err != nil {
		t.Fatal(err)
	}
	if instance.LinkPrefix == "" || fill["link_prefix"] != instance.LinkPrefix {
		t.Fatalf("missing link prefix was not generated: instance=%+v fill=%v", instance, fill)
	}
	if err := st.SetSettings(ctx, fill); err != nil {
		t.Fatal(err)
	}
	stored := app.InstanceConfig{PublicURL: "https://example.com"}
	fill, err = ensureEdgeSecrets(ctx, st, &stored)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LinkPrefix != instance.LinkPrefix || len(fill) != 0 {
		t.Fatalf("stored link prefix = %q, fill=%v; want %q and no writes", stored.LinkPrefix, fill, instance.LinkPrefix)
	}
}

func TestStepFromJSValidatesAndConverts(t *testing.T) {
	at := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC).UnixMilli()
	until := at + 30_000
	event := makeStepEvent("open", at)
	event.Set("generation", float64(1<<53-1))
	event.Set("certSerial", "0a1b")
	event.Set("certNotAfterUnix", int64(1_800_000_000))
	in, gotUntil, err := stepFromJS(makeStepArgs("node-a", "", true, until, event))
	if err != nil {
		t.Fatal(err)
	}
	if in.NodeID != "node-a" || in.State != "" || in.Kind != fleet.LinkOpen || in.Generation != 1<<53-1 ||
		in.CertSerial != "0a1b" || in.CertNotAfter.Unix() != 1_800_000_000 || !in.At.Equal(time.UnixMilli(at)) || !gotUntil.Equal(time.UnixMilli(until)) {
		t.Fatalf("open conversion = %+v, until %v", in, gotUntil)
	}

	frame := []byte{0, 1, 255}
	frameEvent := makeStepEvent("frame", at)
	frameEvent.Set("frame", bytesToJS(frame))
	in, _, err = stepFromJS(makeStepArgs("node-a", "state", false, until, frameEvent))
	if err != nil || in.Kind != fleet.LinkFrame || in.State != "state" || string(in.Frame) != string(frame) {
		t.Fatalf("frame conversion = %+v, %v", in, err)
	}

	requestEvent := makeStepEvent("request", at)
	requestEvent.Set("frame", bytesToJS(frame))
	requestEvent.Set("requestId", "req_test")
	requestEvent.Set("deadlineAt", at+5_000)
	in, _, err = stepFromJS(makeStepArgs("node-a", "state", false, until, requestEvent))
	if err != nil || in.Kind != fleet.LinkRequest || in.RequestID != "req_test" || !in.DeadlineAt.Equal(time.UnixMilli(at+5_000)) {
		t.Fatalf("request conversion = %+v, %v", in, err)
	}

	closedEvent := makeStepEvent("closed", at)
	closedEvent.Set("owned", js.Global().Get("Object").New())
	in, _, err = stepFromJS(makeStepArgs("node-a", "state", false, until, closedEvent))
	if err != nil || in.Kind != fleet.LinkClosed {
		t.Fatalf("closed.owned should be ignored: %+v, %v", in, err)
	}

	for _, generation := range []float64{0, float64(1 << 53), 1.5, math.NaN(), math.Inf(1)} {
		t.Run("generation", func(t *testing.T) {
			bad := makeStepEvent("open", at)
			bad.Set("generation", generation)
			bad.Set("certSerial", "0a1b")
			bad.Set("certNotAfterUnix", int64(1_800_000_000))
			if _, _, err := stepFromJS(makeStepArgs("node-a", "", true, until, bad)); !errors.Is(err, errLinkArgs) {
				t.Fatalf("generation %v error = %v, want errLinkArgs", generation, err)
			}
		})
	}

	badFrame := makeStepEvent("frame", at)
	badFrame.Set("frame", js.Null())
	if _, _, err := stepFromJS(makeStepArgs("node-a", "", true, until, badFrame)); !errors.Is(err, errLinkArgs) {
		t.Fatalf("missing frame error = %v, want errLinkArgs", err)
	}
}

func TestLinkOutToJSOptionalFields(t *testing.T) {
	zero := linkOutToJS(fleet.LinkOut{State: "state"}, newEdgeTaskRunner())
	if zero.Get("state").String() != "state" || zero.Get("frames").Length() != 0 || hasJSProperty(zero, "close") ||
		hasJSProperty(zero, "alarmAt") || hasJSProperty(zero, "replies") || hasJSProperty(zero, "forget") ||
		zero.Get("waitUntil").Type() != js.TypeObject {
		t.Fatalf("zero output conversion has unexpected fields")
	}

	value := linkOutToJS(fleet.LinkOut{
		State:   "next",
		Frames:  [][]byte{{1, 2}},
		Close:   &fleet.LinkClose{Code: 4000, Reason: "retired"},
		AlarmAt: time.UnixMilli(1_800_000_000_123),
		Replies: []fleet.LinkReply{{RequestID: "req_nil"}, {RequestID: "req_bytes", Frame: []byte{3}}},
		Forget:  true,
	}, newEdgeTaskRunner())
	if value.Get("state").String() != "next" || value.Get("frames").Length() != 1 ||
		!value.Get("frames").Index(0).InstanceOf(js.Global().Get("Uint8Array")) ||
		value.Get("close").Get("code").Int() != 4000 || value.Get("close").Get("reason").String() != "retired" ||
		value.Get("alarmAt").Int() != 1_800_000_000_123 ||
		value.Get("replies").Length() != 2 || !value.Get("replies").Index(0).Get("frame").IsNull() ||
		!value.Get("replies").Index(1).Get("frame").InstanceOf(js.Global().Get("Uint8Array")) || value.Get("forget").Bool() != true {
		t.Fatalf("full output conversion lost fields")
	}
}

func newEdgeLinkFixture(t *testing.T) (context.Context, *store.Store, *fleet.Fleet, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.OpenD1(ctx, js.Global().Get("__d1"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	vlt, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	fl, err := fleet.New(st, vlt, builtin.Registry(), fleet.Config{
		AgentSNI:   "agent.example.com",
		LinkServed: true,
		Desired:    func(context.Context, string) ([]statehash.Inbound, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeID := store.NewID("nod_")
	now := time.Now().UTC().Truncate(time.Second)
	name := "node-a-" + nodeID[len("nod_"):]
	if _, err := st.CreateEnrollment(ctx, &store.NodeRow{ID: nodeID, Name: name, Address: "node-a.example.com"}, "",
		[]byte("fake-token-hash-"+nodeID), "adm_test", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	previous := state
	stateMu.Lock()
	state = &edgeState{store: st, fleet: fl, afterResponse: newEdgeTaskRunner()}
	stateMu.Unlock()
	t.Cleanup(func() {
		stateMu.Lock()
		state = previous
		stateMu.Unlock()
	})
	return ctx, st, fl, nodeID
}

func countEdgeD1(t *testing.T, label string, run func() (js.Value, error)) (js.Value, int, error) {
	t.Helper()
	d1 := js.Global().Get("__d1")
	d1.Call("__beginQueryCount", label)
	value, err := run()
	counts := d1.Call("__endQueryCount")
	return value, counts.Get("sequentialQueries").Int(), err
}

func callStep(nodeID, previousState string, stateIsNull bool, until int64, event js.Value) (js.Value, error) {
	return link([]js.Value{js.ValueOf("step"), makeStepArgs(nodeID, previousState, stateIsNull, until, event)})
}

func newLiveEdgeLink(t *testing.T) (string, string) {
	t.Helper()
	_, _, _, nodeID := newEdgeLinkFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	until := now.Add(time.Minute).UnixMilli()
	open := makeStepEvent("open", now.UnixMilli())
	open.Set("generation", 1)
	open.Set("certSerial", "0a1b")
	open.Set("certNotAfterUnix", now.Add(90*24*time.Hour).Unix())
	opened, err := callStep(nodeID, "", true, until, open)
	if err != nil {
		t.Fatalf("open step: %v", err)
	}

	helloFrame, err := proto.Marshal(&agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		ApiVersion: fleet.APIVersion, InstanceId: "instance-a",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	hello := makeStepEvent("frame", now.Add(time.Second).UnixMilli())
	hello.Set("frame", bytesToJS(helloFrame))
	live, err := callStep(nodeID, opened.Get("state").String(), false, until, hello)
	if err != nil {
		t.Fatalf("Hello step: %v", err)
	}
	return nodeID, live.Get("state").String()
}

func TestLinkRefusesPastUntilBeforeD1(t *testing.T) {
	nodeID, stateText := newLiveEdgeLink(t)
	event := makeStepEvent("closed", time.Now().UTC().UnixMilli())
	_, calls, err := countEdgeD1(t, "expired link step", func() (js.Value, error) {
		return callStep(nodeID, stateText, false, time.Now().Add(-time.Second).UnixMilli(), event)
	})
	if !errors.Is(err, errLinkBudget) || calls != 0 {
		t.Fatalf("past until: error=%v D1 calls=%d, want budget error and no D1", err, calls)
	}

	_, calls, err = countEdgeD1(t, "live close baseline", func() (js.Value, error) {
		return callStep(nodeID, stateText, false, time.Now().Add(20*time.Second).UnixMilli(), event)
	})
	if err != nil || calls != 1 {
		t.Fatalf("live closed step: error=%v D1 calls=%d, want one D1 call", err, calls)
	}
}

func TestLinkRefusesUntilWithinTwoSecondsBeforeD1(t *testing.T) {
	nodeID, stateText := newLiveEdgeLink(t)
	event := makeStepEvent("closed", time.Now().UTC().UnixMilli())
	until := time.Now().Add(2 * time.Second).UnixMilli()
	args := makeStepArgs(nodeID, stateText, false, until, event)
	remaining := time.UnixMilli(until).Sub(time.Now())
	if remaining <= 0 || remaining > 2*time.Second {
		t.Fatalf("test until has %v remaining, want (0, 2s]", remaining)
	}
	_, calls, err := countEdgeD1(t, "short link step budget", func() (js.Value, error) {
		return link([]js.Value{js.ValueOf("step"), args})
	})
	if !errors.Is(err, errLinkBudget) || calls != 0 {
		t.Fatalf("until within two seconds: error=%v D1 calls=%d, want budget error and no D1", err, calls)
	}
}

func TestLinkAcceptRefusesPastUntil(t *testing.T) {
	_, _, _, nodeID := newEdgeLinkFixture(t)
	args := js.Global().Get("Object").New()
	args.Set("nodeId", nodeID)
	args.Set("audience", "example.com")
	args.Set("nonce", bytesToJS([]byte{1}))
	args.Set("auth", bytesToJS([]byte{2}))
	args.Set("until", time.Now().Add(-time.Second).UnixMilli())
	_, calls, err := countEdgeD1(t, "expired link accept", func() (js.Value, error) {
		return link([]js.Value{js.ValueOf("accept"), args})
	})
	if !errors.Is(err, errLinkBudget) || calls != 0 {
		t.Fatalf("past accept until: error=%v D1 calls=%d, want budget error and no D1", err, calls)
	}
}

func TestLinkContextDeadline(t *testing.T) {
	t.Run("15 second cap", func(t *testing.T) {
		before := time.Now()
		ctx, cancel, err := linkContext("node-a", before.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		defer cancel()
		after := time.Now()
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Before(before.Add(15*time.Second)) || deadline.After(after.Add(15*time.Second)) {
			t.Fatalf("deadline = %v, want now + 15s inside [%v, %v]", deadline, before.Add(15*time.Second), after.Add(15*time.Second))
		}
	})

	t.Run("until minus two seconds", func(t *testing.T) {
		before := time.Now()
		until := before.Add(10 * time.Second)
		ctx, cancel, err := linkContext("node-a", until)
		if err != nil {
			t.Fatal(err)
		}
		defer cancel()
		deadline, ok := ctx.Deadline()
		want := until.Add(-2 * time.Second)
		if !ok || !deadline.Equal(want) {
			t.Fatalf("deadline = %v, want until - 2s = %v", deadline, want)
		}
	})
}

func TestLinkD1CallsPerStepKind(t *testing.T) {
	_, _, _, nodeID := newEdgeLinkFixture(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	until := now.Add(20 * time.Second).UnixMilli()
	call := func(label, kind, previous string, stateIsNull bool, at time.Time, add func(js.Value)) (js.Value, int) {
		t.Helper()
		event := makeStepEvent(kind, at.UnixMilli())
		if add != nil {
			add(event)
		}
		out, calls, err := countEdgeD1(t, label, func() (js.Value, error) {
			return callStep(nodeID, previous, stateIsNull, until, event)
		})
		if err != nil {
			t.Fatalf("%s step: %v", kind, err)
		}
		return out, calls
	}

	opened, calls := call("open", "open", "", true, now, func(event js.Value) {
		event.Set("generation", 1)
		event.Set("certSerial", "0a1b")
		event.Set("certNotAfterUnix", now.Add(90*24*time.Hour).Unix())
	})
	if calls != 0 {
		t.Fatalf("open used %d D1 calls, want 0", calls)
	}
	stateText := opened.Get("state").String()

	if _, calls = call("desired before Hello", "desired", stateText, false, now.Add(time.Second), nil); calls != 0 {
		t.Fatalf("desired used %d D1 calls, want 0", calls)
	}
	if _, calls = call("alarm before Hello", "alarm", stateText, false, now.Add(2*time.Second), nil); calls != 0 {
		t.Fatalf("alarm used %d D1 calls, want 0", calls)
	}
	requestFrame, err := proto.Marshal(&agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{RequestId: "req_test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, calls = call("request before Hello", "request", stateText, false, now.Add(3*time.Second), func(event js.Value) {
		event.Set("requestId", "req_test")
		event.Set("deadlineAt", now.Add(10*time.Second).UnixMilli())
		event.Set("frame", bytesToJS(requestFrame))
	}); calls != 0 {
		t.Fatalf("request used %d D1 calls, want 0", calls)
	}

	helloFrame, err := proto.Marshal(&agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: &agentv1.Hello{
		ApiVersion: fleet.APIVersion, InstanceId: "instance-a",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	hello, calls := call("Hello frame", "frame", stateText, false, now.Add(4*time.Second), func(event js.Value) {
		event.Set("frame", bytesToJS(helloFrame))
	})
	// A first Hello with no inbounds: NodeHello, Node, NodeWithSentDigest, NodeDesired. The connect-event read and
	// insert come only with a reconnect after a gap, and each inbound adds its access reads (§7.7: up to 6 + k).
	if calls != 4 {
		t.Fatalf("Hello used %d D1 calls, want 4 (first connect, no inbounds)", calls)
	}

	statsFrame, err := proto.Marshal(&agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Add(5 * time.Second).Unix(),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	updated, calls := call("stats frame", "frame", hello.Get("state").String(), false, now.Add(5*time.Second), func(event js.Value) {
		event.Set("frame", bytesToJS(statsFrame))
	})
	if calls != 2 {
		t.Fatalf("stats used %d D1 calls, want 2", calls)
	}

	_, calls = call("closed", "closed", updated.Get("state").String(), false, now.Add(6*time.Second), func(event js.Value) {
		event.Set("owned", false)
	})
	if calls != 1 {
		t.Fatalf("closed used %d D1 calls, want 1", calls)
	}
}

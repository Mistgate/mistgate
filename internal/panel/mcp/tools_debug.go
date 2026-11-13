//go:build !js

package mcp

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/gen/mistgate/ops/v1/opsv1connect"
)

// The on-demand debug capture of one node: node_debug_capture_plan / _apply start it (the node raises its log level for the chosen
// sources for up to 15 minutes and keeps the records in a bounded buffer), node_debug_logs reads what it shipped. Admin profile
// only. The records are cleaned on the node (addresses, names for xray, key-shaped text, an attribute allow-list), again by the
// panel on intake and on the way out, and once more here: the result never carries a client address, a secret name or a key.

const (
	debugDefaultSeconds = 300
	debugMaxSeconds     = 900
)

var debugSource = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

func debugTools() []toolDef {
	out := []toolDef{
		readTool("node_debug_logs", ProfileAdmin, procs(adminv1connect.NodeServiceListNodesProcedure, opsv1connect.OpsServiceListDebugCapturesProcedure, opsv1connect.OpsServiceGetDebugCaptureProcedure),
			"Read the debug capture of a node (started with node_debug_capture_plan / _apply), oldest record first. Name a capture (capture_id) or a node (its newest capture; without either you get the list of captures the panel holds, 24 hours, newest first). "+
				"The capture says its state: running (the node is still capturing; the records arrive when its time is up), done, expired (the time passed and the node never shipped) or failed. "+
				"Filters: min_level (debug, info, warn, error), source (one log source such as hysteria2, xray or agent), query (substring of the message or attribute values, at most 100 characters) and limit (100 by default, at most 500). "+
				"next_cursor continues the walk: pass it back as cursor. Every record is cleaned on the node and again in the panel: no client address (<ip>), no secret name, no key, only a short list of attributes.",
			func(c *call, in debugLogsArg) (any, error) { return c.debugLogs(in) }),
	}
	return append(out, change(debugCapture)...)
}

type debugLogsArg struct {
	CaptureID string `json:"capture_id,omitempty" jsonschema:"a capture id from node_debug_capture_apply or from the list"`
	Node      string `json:"node,omitempty" jsonschema:"node id or exact name: its newest capture; with neither capture_id nor node you get the list"`
	MinLevel  string `json:"min_level,omitempty" jsonschema:"debug, info, warn or error; empty = everything"`
	Source    string `json:"source,omitempty" jsonschema:"one log source, for example hysteria2, xray or agent"`
	Query     string `json:"query,omitempty" jsonschema:"substring of the message or of an attribute value; at most 100 characters"`
	Limit     int    `json:"limit,omitempty" jsonschema:"default 100, at most 500"`
	Cursor    int64  `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page"`
}

// DebugCaptureV is one capture as the tools show it.
type DebugCaptureV struct {
	CaptureID string   `json:"capture_id"`
	Node      string   `json:"node"`
	State     string   `json:"state"`
	Level     string   `json:"level"`
	Sources   []string `json:"sources,omitempty"`
	Seconds   uint32   `json:"duration_s"`
	StartedAt int64    `json:"started_unix"`
	EndsAt    int64    `json:"ends_unix"`
	Actor     string   `json:"started_by,omitempty"`
	Records   uint32   `json:"records"`
	Dropped   uint32   `json:"dropped,omitempty"`
	EndReason string   `json:"end_reason,omitempty"`
}

// DebugLogsV is the node_debug_logs result: a list of captures, or one capture with its records.
type DebugLogsV struct {
	Captures   []DebugCaptureV `json:"captures,omitempty"`
	Capture    *DebugCaptureV  `json:"capture,omitempty"`
	Sources    []string        `json:"sources_seen,omitempty"`
	NextCursor int64           `json:"next_cursor,omitempty"`
	Records    []debugRecordV  `json:"records,omitempty"`
}

type debugRecordV struct {
	Seq     int64             `json:"seq"`
	TimeMs  int64             `json:"time_ms"`
	Level   string            `json:"level"`
	Source  string            `json:"source"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

func debugCaptureView(i *opsv1.DebugCaptureInfo) DebugCaptureV {
	v := DebugCaptureV{CaptureID: clean(i.GetCaptureId(), 64), Node: clean(i.GetNodeName(), 64), State: clean(i.GetState(), 16), Level: clean(i.GetLevel(), 8),
		Seconds: i.GetDurationS(), StartedAt: i.GetStartedUnix(), EndsAt: i.GetEndsUnix(), Actor: clean(i.GetActor(), 64),
		Records: i.GetRecords(), Dropped: i.GetDropped(), EndReason: clean(i.GetEndReason(), 32)}
	for _, s := range i.GetSources() {
		v.Sources = append(v.Sources, clean(s, 32))
	}
	return v
}

func (c *call) debugLogs(in debugLogsArg) (any, error) {
	if in.Cursor < 0 {
		return nil, errors.New("cursor must not be negative")
	}
	if in.CaptureID != "" && in.Node != "" {
		return nil, errors.New("name a capture or a node, not both")
	}
	if in.CaptureID == "" && in.Node == "" {
		r, err := c.cl.Ops.ListDebugCaptures(c.ctx, connect.NewRequest(&opsv1.ListDebugCapturesRequest{}))
		if err != nil {
			return nil, apiError(err)
		}
		v := &DebugLogsV{Captures: []DebugCaptureV{}}
		for _, i := range r.Msg.GetCaptures() {
			v.Captures = append(v.Captures, debugCaptureView(i))
		}
		return v, nil
	}
	req := &opsv1.GetDebugCaptureRequest{MinLevel: in.MinLevel, Source: in.Source, Query: in.Query,
		Limit: uint32(min(max(in.Limit, 0), 500)), AfterSeq: in.Cursor}
	if in.CaptureID != "" {
		if err := validID("capture_id", in.CaptureID); err != nil {
			return nil, err
		}
		req.CaptureId = in.CaptureID
	} else {
		id, _, err := c.nodeRef(in.Node)
		if err != nil {
			return nil, err
		}
		req.NodeId = id
	}
	r, err := c.cl.Ops.GetDebugCapture(c.ctx, connect.NewRequest(req))
	if err != nil {
		return nil, apiError(err)
	}
	cv := debugCaptureView(r.Msg.GetCapture())
	v := &DebugLogsV{Capture: &cv, NextCursor: r.Msg.GetNextCursor(), Records: []debugRecordV{}}
	for _, s := range r.Msg.GetSources() {
		v.Sources = append(v.Sources, clean(s, 32))
	}
	for _, e := range r.Msg.GetRecords() {
		rec := debugRecordV{Seq: e.GetSeq(), TimeMs: e.GetTimeUnixMs(), Level: clean(e.GetLevel(), 8), Source: clean(e.GetSource(), 32), Message: clean(e.GetMessage(), 300)}
		for k, val := range scrubDebugCaptureAttrs(e.GetAttrs()) {
			if rec.Attrs == nil {
				rec.Attrs = map[string]string{}
			}
			rec.Attrs[clean(k, 40)] = clean(val, 200)
		}
		v.Records = append(v.Records, rec)
	}
	return v, nil
}

type debugCaptureArgs struct {
	ReasonField
	Node     string   `json:"node" jsonschema:"node id or exact name"`
	Level    string   `json:"level,omitempty" jsonschema:"debug (default) or info: the lowest level the capture keeps"`
	Sources  []string `json:"sources,omitempty" jsonschema:"log sources to capture, at most 8 (agent, hysteria2, xray, awg, kernel); kernel is the node kernel log from 30 minutes before start through the capture, with firewall packet lines counted but not kept; awg on a kernel-backend node enables module debug and adds a summary with unanswered initiations; empty = all"`
	Duration int      `json:"duration_s,omitempty" jsonschema:"seconds, 1 to 900; default 300"`
}

var debugCapture = changeSpec[debugCaptureArgs]{
	name: "node_debug_capture", min: ProfileAdmin,
	procs: procs(adminv1connect.NodeServiceListNodesProcedure, opsv1connect.OpsServiceStartDebugCaptureProcedure),
	desc: "Capture one node's debug log for a few minutes: the node raises its log level for the chosen sources, keeps up to 20000 records (4 MiB) and ships them to the panel when the time is up (at most 15 minutes); " +
		"then it puts the level back, and a restart of the agent ends the capture too. Read the result with node_debug_logs. The node must be online and its agent must support it (an older agent is refused). " +
		"The kernel source reads the node's kernel log from 30 minutes before capture start through the end; firewall packet-log lines are counted in the summary and not kept. " +
		"On a kernel-backend node, choosing awg turns on the module's debug output for the capture and adds a summary with handshake initiations that had no response within 5 seconds. " +
		"One capture per node at a time. Records are cleaned on the node and in the panel: client addresses, secret names, keys and (for xray) host names are cut out. The start is audit-logged with who asked, the node, the sources and the duration.",
	plan: func(c *call, a debugCaptureArgs) (*planned, error) {
		level := a.Level
		if level == "" {
			level = "debug"
		}
		if level != "debug" && level != "info" {
			return nil, errors.New("level must be debug or info")
		}
		secs := a.Duration
		if secs == 0 {
			secs = debugDefaultSeconds
		}
		if secs < 1 || secs > debugMaxSeconds {
			return nil, errors.New("duration_s must be from 1 to 900")
		}
		if len(a.Sources) > 8 {
			return nil, errors.New("at most 8 sources")
		}
		for _, s := range a.Sources {
			if !debugSource.MatchString(s) {
				return nil, errors.New("a source is 1 to 32 letters, digits, - or _")
			}
		}
		id, name, err := c.nodeRef(a.Node)
		if err != nil {
			return nil, err
		}
		sources := "all"
		if len(a.Sources) > 0 {
			sources = strings.Join(a.Sources, ", ")
		}
		return &planned{
			Summary: "Capture one node's debug log for a short time. The node returns to its normal level by itself; nothing is stored beyond 24 hours.",
			Facts: []Fact{
				{Key: "node", Value: nm(name), Untrusted: true}, {Key: "level", Value: level}, {Key: "sources", Value: sources},
				{Key: "duration", Value: strconv.Itoa(secs) + " s"}, {Key: "limits", Value: "20000 records, 4 MiB"},
			},
			Params: debugCaptureArgs{Node: id, Level: level, Sources: a.Sources, Duration: secs},
		}, nil
	},
	apply: func(c *call, a debugCaptureArgs, _ Plan) (done, error) {
		r, err := c.cl.Ops.StartDebugCapture(c.ctx, connect.NewRequest(&opsv1.StartDebugCaptureRequest{
			NodeId: a.Node, Level: a.Level, Sources: a.Sources, DurationS: uint32(a.Duration)}))
		if err != nil {
			return done{}, apiError(err)
		}
		i := r.Msg.GetCapture()
		ends := time.Unix(i.GetEndsUnix(), 0).UTC().Format(time.RFC3339)
		return doneWith("debug_capture_started", "Debug capture started on "+clean(i.GetNodeName(), 64)+" until "+ends+". Read it with node_debug_logs (capture_id "+clean(i.GetCaptureId(), 64)+") after that time.",
			"capture_id", clean(i.GetCaptureId(), 64), "ends_unix", strconv.FormatInt(i.GetEndsUnix(), 10)), nil
	},
}

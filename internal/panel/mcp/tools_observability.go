//go:build !js

package mcp

import (
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/gen/mistgate/ops/v1/opsv1connect"
)

// The panel's own diagnostics (internal/panel/ops): its log records, the log level and the retention of what it stores. Admin
// profile only: the logs are the panel's text, scrubbed by the ops service (and again here) but still an operator's view. The
// change tools are plan/apply: the log level is reversible and resets on restart, a shorter retention deletes rows for good and
// needs the owner's approval.

func observabilityTools() []toolDef {
	dbg := debugTools()
	out := []toolDef{
		readTool("panel_logs", ProfileAdmin, procs(opsv1connect.OpsServiceGetPanelLogsProcedure),
			"The panel's own recent log records (an in-memory ring, lost on restart), newest first, with addresses, secret names and key-shaped values removed and only allow-listed attributes. "+
				"Filters: min_level (debug, info, warn, error; info by default), minutes (window back from now, up to 10080), query (substring of the message or attribute values, at most 100 characters), "+
				"component (the part of the panel: health, fleet, http ...), code (an exact error code), request_id (the code the admin shows on a failed call) and limit (100 by default, at most 500). "+
				"next_cursor continues the walk: pass it back as cursor. level, buffered and capacity say what the ring holds; debug records exist only while the level is debug (log_level_plan).",
			func(c *call, in panelLogsArg) (any, error) {
				if in.Minutes < 0 || in.Minutes > 10080 {
					return nil, errors.New("minutes must be from 1 to 10080")
				}
				req := &opsv1.GetPanelLogsRequest{
					MinLevel: in.MinLevel, Query: in.Query, Component: in.Component, Code: in.Code, RequestId: in.RequestID,
					Limit: uint32(min(max(in.Limit, 0), 500)), BeforeSeq: max(in.Cursor, 0),
				}
				if in.Minutes > 0 {
					req.SinceUnix = c.e.now().Add(-time.Duration(in.Minutes) * time.Minute).Unix()
				}
				r, err := c.cl.Ops.GetPanelLogs(c.ctx, connect.NewRequest(req))
				if err != nil {
					return nil, apiError(err)
				}
				return panelLogsView(r.Msg), nil
			}),
		readTool("retention_status", ProfileAdmin, procs(opsv1connect.OpsServiceGetRetentionProcedure),
			"How long the panel keeps what it stores: per setting its value, default, limits and unit (journal_days, events_info_days, events_warn_days, audit_days, alerts_days, check_samples_hours, check_daily_days, host_history_days, traffic_bucket_days, node_traffic_hour_days). Change with retention_plan.",
			func(c *call, _ noArgs) (any, error) {
				r, err := c.cl.Ops.GetRetention(c.ctx, connect.NewRequest(&opsv1.GetRetentionRequest{}))
				if err != nil {
					return nil, apiError(err)
				}
				return retentionView(r.Msg.GetItems()), nil
			}),
	}
	out = append(out, change(logLevel)...)
	out = append(out, change(retentionSet)...)
	out = append(out, dbg...)
	return out
}

type panelLogsArg struct {
	MinLevel  string `json:"min_level,omitempty" jsonschema:"debug, info, warn or error; empty = info"`
	Minutes   int    `json:"minutes,omitempty" jsonschema:"window back from now in minutes, 1 to 10080; empty = everything held"`
	Query     string `json:"query,omitempty" jsonschema:"substring of the message or of an attribute value; at most 100 characters"`
	Component string `json:"component,omitempty" jsonschema:"the part of the panel that logged, for example health or http"`
	Code      string `json:"code,omitempty" jsonschema:"an exact error code attribute"`
	RequestID string `json:"request_id,omitempty" jsonschema:"a request id as the admin shows it on a failed call"`
	Limit     int    `json:"limit,omitempty" jsonschema:"default 100, at most 500"`
	Cursor    int64  `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page"`
}

// PanelLogsV is the panel_logs result.
type PanelLogsV struct {
	Level      string          `json:"level"`
	Buffered   uint32          `json:"buffered"`
	Capacity   uint32          `json:"capacity"`
	NextCursor int64           `json:"next_cursor,omitempty"`
	Entries    []panelLogEntry `json:"entries"`
	Truncated  bool            `json:"truncated,omitempty"`
}

func (v *PanelLogsV) shrink() bool {
	v.Truncated = true
	ok := cut(&v.Entries)
	if n := len(v.Entries); n > 0 {
		v.NextCursor = v.Entries[n-1].Seq
	}
	return ok
}

type panelLogEntry struct {
	Seq       int64             `json:"seq"`
	TimeMs    int64             `json:"time_ms"`
	Level     string            `json:"level"`
	Component string            `json:"component,omitempty"`
	Message   string            `json:"message"`
	Code      string            `json:"code,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

func panelLogsView(r *opsv1.GetPanelLogsResponse) *PanelLogsV {
	v := &PanelLogsV{Level: r.GetLevel(), Buffered: r.GetBuffered(), Capacity: r.GetCapacity(), NextCursor: r.GetNextCursor(), Entries: []panelLogEntry{}}
	for _, e := range r.GetEntries() {
		item := panelLogEntry{Seq: e.GetSeq(), TimeMs: e.GetTimeUnixMs(), Level: e.GetLevel(), Component: clean(e.GetComponent(), 40),
			Message: clean(e.GetMessage(), 300), Code: clean(e.GetCode(), 64), RequestID: clean(e.GetRequestId(), 32)}
		for k, val := range e.GetAttrs() {
			if item.Attrs == nil {
				item.Attrs = map[string]string{}
			}
			item.Attrs[clean(k, 40)] = clean(val, 200)
		}
		v.Entries = append(v.Entries, item)
	}
	return v
}

// RetentionV is the retention_status result.
type RetentionV struct {
	Settings []retentionItemV `json:"settings"`
}

type retentionItemV struct {
	Key     string `json:"key"`
	Value   uint32 `json:"value"`
	Default uint32 `json:"default"`
	Min     uint32 `json:"min"`
	Max     uint32 `json:"max"`
	Unit    string `json:"unit"`
}

func retentionView(items []*opsv1.RetentionItem) *RetentionV {
	v := &RetentionV{Settings: []retentionItemV{}}
	for _, i := range items {
		v.Settings = append(v.Settings, retentionItemV{Key: i.GetKey(), Value: i.GetValue(), Default: i.GetDefaultValue(), Min: i.GetMin(), Max: i.GetMax(), Unit: i.GetUnit()})
	}
	return v
}

type logLevelArgs struct {
	ReasonField
	Level string `json:"level" jsonschema:"debug, info, warn or error; the panel returns to its configured level on restart"`
}

var logLevel = changeSpec[logLevelArgs]{
	name: "log_level", min: ProfileAdmin,
	procs: procs(opsv1connect.OpsServiceGetLogSettingsProcedure, opsv1connect.OpsServiceSetLogLevelProcedure),
	desc:  "Set the panel's log level until its next restart (debug shows the most; warn and error keep little). It removes nothing and the ring keeps only the last records, so raise it to debug, reproduce the problem, read panel_logs and lower it again.",
	plan: func(c *call, a logLevelArgs) (*planned, error) {
		if !validLogLevel(a.Level) {
			return nil, errors.New("level must be debug, info, warn or error")
		}
		r, err := c.cl.Ops.GetLogSettings(c.ctx, connect.NewRequest(&opsv1.GetLogSettingsRequest{}))
		if err != nil {
			return nil, apiError(err)
		}
		return &planned{Summary: "Change the panel's log level until it restarts. Nothing is deleted.", Facts: []Fact{
			{Key: "from", Value: r.Msg.GetLevel()}, {Key: "to", Value: a.Level}, {Key: "default_after_restart", Value: r.Msg.GetDefaultLevel()},
		}, Params: a}, nil
	},
	apply: func(c *call, a logLevelArgs, _ Plan) (done, error) {
		r, err := c.cl.Ops.SetLogLevel(c.ctx, connect.NewRequest(&opsv1.SetLogLevelRequest{Level: a.Level}))
		if err != nil {
			return done{}, apiError(err)
		}
		return doneWith("log_level_changed", "Log level set to "+r.Msg.GetLevel()+" until the panel restarts.", "level", r.Msg.GetLevel()), nil
	},
}

func validLogLevel(l string) bool { return l == "debug" || l == "info" || l == "warn" || l == "error" }

type retentionArgs struct {
	ReasonField
	Values map[string]uint32 `json:"values" jsonschema:"setting name to its new value (see retention_status for names, units and limits); only the settings to change"`
}

var retentionSet = changeSpec[retentionArgs]{
	name: "retention", min: ProfileAdmin, danger: true,
	procs: procs(opsv1connect.OpsServiceGetRetentionProcedure, opsv1connect.OpsServiceSetRetentionProcedure),
	desc:  "Change how long the panel keeps journal, events, audit, alerts, check results, host history and hourly traffic. A shorter time deletes the older rows at the next hourly sweep, for good. Needs the owner's approval in the admin panel.",
	plan: func(c *call, a retentionArgs) (*planned, error) {
		if len(a.Values) == 0 || len(a.Values) > 16 {
			return nil, errors.New("values must name at least one setting")
		}
		r, err := c.cl.Ops.GetRetention(c.ctx, connect.NewRequest(&opsv1.GetRetentionRequest{}))
		if err != nil {
			return nil, apiError(err)
		}
		known := map[string]*opsv1.RetentionItem{}
		for _, i := range r.Msg.GetItems() {
			known[i.GetKey()] = i
		}
		var facts []Fact
		for _, k := range sortedKeys(a.Values) {
			i, ok := known[k]
			if !ok {
				return nil, errors.New("unknown retention setting " + clean(k, 40))
			}
			if v := a.Values[k]; v < i.GetMin() || v > i.GetMax() {
				return nil, errors.New(k + " must be from " + strconv.Itoa(int(i.GetMin())) + " to " + strconv.Itoa(int(i.GetMax())) + " " + i.GetUnit())
			}
			facts = append(facts, Fact{Key: k, Value: strconv.Itoa(int(i.GetValue())) + " -> " + strconv.Itoa(int(a.Values[k])) + " " + i.GetUnit()})
		}
		return &planned{Summary: "Change how long the panel keeps some of its data. Rows older than the new time are deleted at the next hourly sweep.", Facts: facts,
			Danger: []string{dangerStepUp}, Params: a}, nil
	},
	apply: func(c *call, a retentionArgs, _ Plan) (done, error) {
		if _, err := c.cl.Ops.SetRetention(c.ctx, connect.NewRequest(&opsv1.SetRetentionRequest{Values: a.Values})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("retention_changed", "Retention settings saved.", "count", strconv.Itoa(len(a.Values))), nil
	},
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

//go:build !js

package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/retention"
)

func (r rpc) GetRetention(ctx context.Context, _ *connect.Request[opsv1.GetRetentionRequest]) (*connect.Response[opsv1.GetRetentionResponse], error) {
	items, err := r.s.retentionItems(ctx)
	if err != nil {
		return nil, internalErr(r.s.cfg.Log, "read retention", err)
	}
	return connect.NewResponse(&opsv1.GetRetentionResponse{Items: items}), nil
}

func (s *Service) retentionItems(ctx context.Context) ([]*opsv1.RetentionItem, error) {
	stored, err := s.cfg.Store.SettingValues(ctx, retention.Keys())
	if err != nil {
		return nil, err
	}
	pol := retention.Parse(stored)
	items := make([]*opsv1.RetentionItem, len(retention.Fields))
	for i, f := range retention.Fields {
		unit := "days"
		if f.Unit == time.Hour {
			unit = "hours"
		}
		items[i] = &opsv1.RetentionItem{
			Key: f.Key, Value: uint32(pol.Values[i]), DefaultValue: uint32(f.Default), Min: uint32(f.Min), Max: uint32(f.Max), Unit: unit,
		}
	}
	return items, nil
}

func (r rpc) SetRetention(ctx context.Context, req *connect.Request[opsv1.SetRetentionRequest]) (*connect.Response[opsv1.SetRetentionResponse], error) {
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if len(req.Msg.Values) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("nothing to change"))
	}
	byKey := map[string]retention.Field{}
	for _, f := range retention.Fields {
		byKey[f.Key] = f
	}
	set := map[string]string{}
	var reset []string
	changed := map[string]string{}
	keys := make([]string, 0, len(req.Msg.Values))
	for k := range req.Msg.Values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := int(req.Msg.Values[k])
		f, ok := byKey[k]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown retention setting"))
		}
		if !f.Valid(v) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("retention_out_of_range: key=%s&min=%d&max=%d", k, f.Min, f.Max))
		}
		changed[k] = strconv.Itoa(v)
		if v == f.Default {
			reset = append(reset, retention.Prefix+k) // the default is "no row": a later change of the default reaches this panel
		} else {
			set[retention.Prefix+k] = strconv.Itoa(v)
		}
	}
	if err := s.cfg.Store.SetAndDeleteSettings(ctx, set, reset...); err != nil {
		return nil, internalErr(s.cfg.Log, "save retention", err)
	}
	s.audit(ctx, "retention_set", changed)
	items, err := s.retentionItems(ctx)
	if err != nil {
		return nil, internalErr(s.cfg.Log, "read retention", err)
	}
	return connect.NewResponse(&opsv1.SetRetentionResponse{Items: items}), nil
}

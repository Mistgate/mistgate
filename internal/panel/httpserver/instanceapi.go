package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// instanceAPI implements InstanceService on top of package instance.
type instanceAPI struct {
	st                *store.Store
	log               *slog.Logger
	adminURL, subBase string // shown to the owner only
}

// forOwner adds the addresses an owner may see to an Instance.
func (a *instanceAPI) forOwner(ctx context.Context, in *adminv1.Instance) *adminv1.Instance {
	if admin, ok := auth.AdminFrom(ctx); ok && admin.Role == store.RoleOwner {
		in.AdminUrl, in.SubscriptionBase = a.adminURL, a.subBase
	}
	return in
}

func (a *instanceAPI) handler() (string, http.Handler) {
	return adminv1connect.NewInstanceServiceHandler(a, connect.WithReadMaxBytes(2*instance.MaxLogoBytes))
}

func toProtoInstance(s instance.Settings) *adminv1.Instance {
	out := &adminv1.Instance{BrandHead: s.BrandHead, BrandTail: s.BrandTail, Accent: s.Accent, Language: s.Language, HasLogo: s.LogoSVG != ""}
	if out.HasLogo {
		out.LogoVersion = logoETag(s.LogoSVG)
	}
	return out
}

func logoETag(svg string) string {
	h := sha256.Sum256([]byte(svg))
	return hex.EncodeToString(h[:6])
}

func (a *instanceAPI) GetInstance(ctx context.Context, _ *connect.Request[adminv1.GetInstanceRequest]) (*connect.Response[adminv1.GetInstanceResponse], error) {
	if _, ok := auth.AdminFrom(ctx); !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not signed in"))
	}
	s, err := instance.Load(ctx, a.st)
	if err != nil {
		a.log.Error("load instance settings", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	return connect.NewResponse(&adminv1.GetInstanceResponse{Instance: a.forOwner(ctx, toProtoInstance(s))}), nil
}

func (a *instanceAPI) UpdateInstance(ctx context.Context, req *connect.Request[adminv1.UpdateInstanceRequest]) (*connect.Response[adminv1.UpdateInstanceResponse], error) {
	admin, ok := auth.AdminFrom(ctx)
	if !ok {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not signed in"))
	}
	if admin.Role != store.RoleOwner {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the owner can change the instance settings"))
	}
	m := req.Msg
	s, err := instance.Update(ctx, a.st, instance.Patch{
		BrandHead: m.BrandHead, BrandTail: m.BrandTail, Accent: m.Accent, Language: m.Language, LogoSVG: m.LogoSvg,
	})
	if errors.Is(err, instance.ErrInvalid) {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New(err.Error()))
	}
	if err != nil {
		a.log.Error("update instance settings", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("internal error"))
	}
	var changed []string
	for _, f := range []struct {
		name string
		set  bool
	}{{"brand_head", m.BrandHead != nil}, {"brand_tail", m.BrandTail != nil}, {"accent", m.Accent != nil}, {"language", m.Language != nil}, {"logo", m.LogoSvg != nil}} {
		if f.set {
			changed = append(changed, f.name)
		}
	}
	params, _ := json.Marshal(map[string]any{"fields": changed})
	entry := store.AuditEntry{Actor: admin.ID, Action: "instance_update", Result: "ok", Params: string(params)}
	if ip := auth.ClientIPFrom(ctx); ip.IsValid() {
		entry.IP = ip.String()
	}
	if err := a.st.Audit(ctx, time.Now(), entry); err != nil {
		a.log.Error("audit write failed", "action", entry.Action, "err", err)
	}
	return connect.NewResponse(&adminv1.UpdateInstanceResponse{Instance: a.forOwner(ctx, toProtoInstance(s))}), nil
}

// NewLogoHandler serves the brand logo without a session: GET/HEAD, image/svg+xml, 404
// when there is no custom logo. The SVG was sanitised on the way in; the headers also
// keep it inert if someone opens it as a page (no script, no sandbox escape) and let the
// browser revalidate by ETag. Mount it wherever a public page needs the logo.
func NewLogoHandler(st *store.Store, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}
		s, err := instance.Load(r.Context(), st)
		if err != nil {
			// A 404, not a 500: under a secret public prefix this must look like any unknown path.
			log.Error("load logo", "err", err)
			http.NotFound(w, r)
			return
		}
		if s.LogoSVG == "" {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "image/svg+xml")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", `"`+logoETag(s.LogoSVG)+`"`)
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader([]byte(s.LogoSVG)))
	})
}

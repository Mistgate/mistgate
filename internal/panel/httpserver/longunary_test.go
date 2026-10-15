package httpserver

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// A long unary admin call (the TSPU probe test) answers after the listener's write timeout; any other unary call is cut at it.
func TestLongUnaryCallOutlivesTheWriteTimeout(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond)
		io.WriteString(w, "done")
	})
	router := apiRouter{
		{Path: "/mistgate.admin.v1.TspuProbeService/", Handler: slow},
		{Path: "/mistgate.admin.v1.HealthService/", Handler: slow},
	}
	ts := httptest.NewUnstartedServer(router)
	ts.EnableHTTP2 = true
	ts.Config.WriteTimeout = 150 * time.Millisecond
	ts.StartTLS()
	t.Cleanup(ts.Close)

	post := func(path string) (string, error) {
		resp, err := ts.Client().Post(ts.URL+path, "application/proto", nil)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}
	if body, err := post(adminv1connect.TspuProbeServiceTestTspuProbeProcedure); err != nil || body != "done" {
		t.Fatalf("the probe test was cut: %q %v", body, err)
	}
	if body, err := post(adminv1connect.HealthServiceGetChecksProcedure); err == nil && body == "done" {
		t.Fatal("an ordinary unary call outlived the write timeout")
	}
}

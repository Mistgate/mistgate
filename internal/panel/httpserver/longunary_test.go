package httpserver

import (
	"context"
	"errors"
	"io"
	"net"
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

func TestLongUnaryRequestBodyKeepsTheReadTimeout(t *testing.T) {
	readErrors := make(chan error, 1)
	bodyRead := make(chan struct{})
	router := apiRouter{{Path: "/mistgate.admin.v1.TspuProbeService/", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(bodyRead)
		_, err := io.ReadAll(r.Body)
		readErrors <- err
	})}}
	ts := httptest.NewUnstartedServer(router)
	ts.Config.ReadTimeout = 150 * time.Millisecond
	ts.Config.WriteTimeout = time.Second
	ts.Start()
	t.Cleanup(ts.Close)

	body, bodyWriter := io.Pipe()
	t.Cleanup(func() { _ = bodyWriter.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.URL+adminv1connect.TspuProbeServiceTestTspuProbeProcedure, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/proto")
	req.ContentLength = 2
	done := make(chan error, 1)
	go func() {
		resp, err := ts.Client().Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()
	if _, err := bodyWriter.Write([]byte("x")); err != nil {
		t.Fatalf("send partial request body: %v", err)
	}
	select {
	case <-bodyRead:
	case <-time.After(time.Second):
		t.Fatal("handler did not start reading the request body")
	}
	select {
	case err := <-readErrors:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("stalled request body error = %v, want a read timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled request body outlived the read timeout")
	}
	_ = bodyWriter.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("request did not finish after the read timeout")
	}
}

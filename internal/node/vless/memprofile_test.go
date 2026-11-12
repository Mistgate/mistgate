//go:build memprofile

package vless

// A memory profile of the VLESS engine (the core inside the node agent), for comparing two cores: scripts/e2e/memprofile.sh runs
// it on a checkout of main (Xray 26.3.27) and on this branch (26.11.9), three runs each. Not part of the normal test run.
//
//	go test -tags memprofile -run TestMemProfile -v -timeout 60m ./internal/node/vless/
//
// MEMPROFILE_XRAY is the Xray client binary that plays the apps (the same one on both sides), MEMPROFILE_OUT a file that gets one
// tab-separated row per phase, MEMPROFILE_LABEL the name of the side and the run, MEMPROFILE_IDLE the idle time (default 10m).
//
// Phases: start (REALITY RAW+Vision inbound with 1 user), users90 (the other 89 added one revision at a time), matrix1..3 (85 clients one after the other, three
// passes in a row), hang500 (500 connections that sent half a ClientHello and are held), released (those closed), churn (every
// user's key replaced one revision at a time, five rounds), idle (after the idle time). Columns: live heap after a GC, bytes obtained from the OS by the Go runtime, goroutines, resident set size.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"github.com/mistgate/mistgate/internal/plugin"
)

func rssKB() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(rest)
			if len(f) > 0 {
				n, _ := strconv.Atoi(f[0])
				return n
			}
		}
	}
	return -1
}

func TestMemProfile(t *testing.T) {
	xrayBin := os.Getenv("MEMPROFILE_XRAY")
	if xrayBin == "" {
		t.Skip("MEMPROFILE_XRAY is not set")
	}
	label := os.Getenv("MEMPROFILE_LABEL")
	idle := 10 * time.Minute
	if v := os.Getenv("MEMPROFILE_IDLE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			t.Fatal(err)
		}
		idle = d
	}
	var out *os.File
	if p := os.Getenv("MEMPROFILE_OUT"); p != "" {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		out = f
	}
	measure := func(phase string) {
		rss := rssKB() // before the GC below: what the process holds as it stands
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		row := fmt.Sprintf("%s\t%s\t%d\t%d\t%d\t%d", label, phase, m.HeapAlloc>>10, m.Sys>>10, runtime.NumGoroutine(), rss)
		t.Log(row)
		if out != nil {
			_, _ = fmt.Fprintln(out, row)
		}
	}

	f := newRuntimeFixture(t, false)
	spec := f.spec("mem", "tcp")
	const users = 90
	creds := make([]plugin.UserCred, users)
	for i := range creds {
		creds[i] = testCredential(fmt.Sprintf("mem-user-%02d", i), fmt.Sprintf("66ad4540-b58c-4ad2-9926-ea63445a%04x", i+1))
	}
	// the panel adds users one revision at a time: one user first (start), then the rest, one Apply each (users90)
	if _, err := f.engine.Apply(context.Background(), spec, creds[:1]); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	measure("start")
	for n := 2; n <= users; n++ {
		if _, err := f.engine.Apply(context.Background(), spec, creds[:n]); err != nil {
			t.Fatal(err)
		}
	}
	measure("users90")

	dir := t.TempDir()
	runClient := func(i int) {
		c := creds[i%users]
		fp := []string{"chrome", "ios", "firefox", "edge"}[i%4]
		sp := reservePort(t)
		conf := map[string]any{
			"log":      map[string]any{"loglevel": "none"},
			"inbounds": []any{map[string]any{"listen": "127.0.0.1", "port": sp, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": false}}},
			"outbounds": []any{map[string]any{"protocol": "vless",
				"settings": map[string]any{"vnext": []any{map[string]any{"address": "127.0.0.1", "port": spec.Listen.Port,
					"users": []any{map[string]any{"id": stringID(c), "encryption": "none", "flow": "xtls-rprx-vision"}}}}},
				"streamSettings": map[string]any{"network": "tcp", "security": "reality",
					"realitySettings": map[string]any{"serverName": "localhost", "fingerprint": fp, "publicKey": testPublicKey, "shortId": "6ba85179", "spiderX": ""}}}},
		}
		data, _ := json.Marshal(conf)
		path := filepath.Join(dir, fmt.Sprintf("c%d.json", i))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(xrayBin, "run", "-c", path)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(sp)))
		for k := 0; k < 100; k++ {
			if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
				_ = c.Close()
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		d, err := proxy.SOCKS5("tcp", addr, nil, proxy.Direct)
		if err != nil {
			t.Fatal(err)
		}
		var conn net.Conn
		for k := 0; k < 3; k++ {
			if conn, err = d.Dial("tcp", echo.Addr().String()); err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			t.Fatalf("client %d (%s): %v", i, fp, err)
		}
		if _, err := exchange(conn, make([]byte, 64<<10)); err != nil {
			t.Fatalf("client %d (%s): %v", i, fp, err)
		}
		_ = conn.Close()
	}
	for pass := 1; pass <= 3; pass++ {
		for i := 0; i < 85; i++ {
			runClient(i)
		}
		time.Sleep(2 * time.Second)
		measure("matrix" + strconv.Itoa(pass))
	}

	// 500 connections that sent half a ClientHello and wait
	hello := realHello(t, nil)
	held := make([]net.Conn, 0, 500)
	for i := 0; i < 500; i++ {
		c, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.Listen.Port))))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write(hello[:len(hello)/2])
		held = append(held, c)
	}
	time.Sleep(5 * time.Second)
	measure("hang500")
	for _, c := range held {
		_ = c.Close()
	}
	time.Sleep(10 * time.Second)
	measure("released")

	// churn: every user's credential replaced, one revision each (a remove and an add), five rounds
	for round := 1; round <= 5; round++ {
		for i := range creds {
			creds[i] = testCredential(fmt.Sprintf("mem-user-%02d", i), fmt.Sprintf("66ad4540-b58c-4ad2-9926-ea63445a%04x", round*1000+i+1))
			if _, err := f.engine.Apply(context.Background(), spec, creds); err != nil {
				t.Fatal(err)
			}
		}
	}
	measure("churn")

	time.Sleep(idle)
	measure("idle")
}

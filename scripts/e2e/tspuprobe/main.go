//go:build linux

// tspuprobe runs the agent's TSPU probe (internal/tspuprobe) once from the command line, for scripts/e2e/tspu.sh, and
// prints one JSON line: the capabilities of the process, whether a raw socket can be opened, the probe's answer.
//
//	tspuprobe -control IP -ref IP [-hop-limit N] [-max-ttl N] host:port [host:port ...]
//
// It sends SYNs only to the addresses it is given. The script runs it in a network namespace of documentation addresses.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/mistgate/mistgate/internal/tspuprobe"
)

type output struct {
	CapEff string            `json:"cap_eff"`
	UID    int               `json:"uid"`
	Raw    string            `json:"raw_socket"`
	DPIHop int               `json:"dpi_hop"`
	Err    string            `json:"error,omitempty"`
	Result map[string]string `json:"verdicts,omitempty"`
}

func main() {
	control := flag.String("control", "", "control target (IPv4)")
	ref := flag.String("ref", "", "blocked reference (IPv4)")
	hopLimit := flag.Int("hop-limit", 0, "hops behind the DPI box")
	maxTTL := flag.Int("max-ttl", 0, "highest TTL of the DPI hop search")
	flag.Parse()

	req := tspuprobe.Request{Control: *control, BlockedRef: *ref, HopLimit: *hopLimit, MaxTTL: *maxTTL}
	ports := map[string][]uint16{}
	for _, arg := range flag.Args() {
		i := strings.LastIndex(arg, ":")
		port, err := strconv.Atoi(arg[i+1:])
		if i < 0 || err != nil {
			fmt.Fprintln(os.Stderr, "target must be host:port:", arg)
			os.Exit(2)
		}
		if _, seen := ports[arg[:i]]; !seen {
			req.Targets = append(req.Targets, tspuprobe.Target{Host: arg[:i]})
		}
		ports[arg[:i]] = append(ports[arg[:i]], uint16(port))
	}
	for i := range req.Targets {
		req.Targets[i].Ports = ports[req.Targets[i].Host]
	}

	out := output{CapEff: capEff(), UID: os.Getuid(), Raw: rawSocket()}
	res, err := tspuprobe.Run(context.Background(), req)
	out.DPIHop, out.Result = res.DPIHop, res.Verdicts
	if err != nil {
		out.Err = err.Error()
		switch {
		case errors.Is(err, tspuprobe.ErrControlFailed):
			out.Err = "control_failed"
		case errors.Is(err, tspuprobe.ErrNoDPIHop):
			out.Err = "no_dpi_hop"
		}
	}
	_ = json.NewEncoder(os.Stdout).Encode(out)
}

// capEff is the effective capability mask of this process, hex.
func capEff() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "unknown"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strings.TrimSpace(v)
		}
	}
	return "unknown"
}

// rawSocket tries what the probe must never need: a raw ICMP socket. "denied" is the expected answer without CAP_NET_RAW.
func rawSocket() string {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_ICMP)
	if err == nil {
		syscall.Close(fd)
		return "allowed"
	}
	if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
		return "denied"
	}
	return err.Error()
}

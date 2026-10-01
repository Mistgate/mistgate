// Command h3get does one HTTP/3 GET with certificate verification off and prints the status line, the response
// headers (sorted) and the start of the body. scripts/e2e-wsl.sh uses it because curl in Ubuntu 24.04 has no
// HTTP/3: it checks that a node answers a non-Hysteria client with the decoy page and no Server header.
package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/apernet/quic-go/http3"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: h3get https://host:port/path")
		os.Exit(2)
	}
	// InsecureSkipVerify: the node under test has a self-signed certificate; this tool only ever runs
	// against a throwaway node on loopback.
	tr := &http3.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	defer tr.Close()
	resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Get(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "h3get:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	fmt.Printf("%s %s\n", resp.Proto, resp.Status)
	keys := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range resp.Header[k] {
			fmt.Printf("%s: %s\n", k, v)
		}
	}
	fmt.Printf("\n%s", body)
}

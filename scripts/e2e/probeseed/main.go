// Command probeseed writes the ASN table that scripts/e2e/probe.sh gives its panel, so the panel can tell the networks of the
// lab's "homes" apart. Every -net is CIDR=ASN=COUNTRY=NAME; the ranges must not overlap. The table is signed into a bundle by
// scripts/e2e/signbundle (the file is named mistgate-asndb-data-any), the panel trusts it through the throwaway release key.
//
//	probeseed -out FILE -net 10.78.13.0/24=64496=RU=ExampleNet -net 10.78.113.0/24=64497=RU=OtherNet
//	probeseed -keygen FILE      writes a throwaway release private key to FILE and prints the public key
package main

import (
	"flag"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/asndb"
	"github.com/mistgate/mistgate/internal/release"
)

type nets []string

func (n *nets) String() string     { return strings.Join(*n, ",") }
func (n *nets) Set(v string) error { *n = append(*n, v); return nil }

func main() {
	var list nets
	out := flag.String("out", "", "output table file")
	flag.Var(&list, "net", "CIDR=ASN=COUNTRY=NAME (repeatable)")
	keygen := flag.String("keygen", "", "write a throwaway release private key to this file and print the public key")
	flag.Parse()
	if *keygen != "" {
		pub, priv, err := release.GenerateKey()
		if err == nil {
			err = os.WriteFile(*keygen, []byte(release.EncodePrivateKey(priv)+"\n"), 0o600)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "probeseed:", err)
			os.Exit(1)
		}
		fmt.Println(release.EncodePublicKey(pub))
		return
	}
	if err := run(*out, list); err != nil {
		fmt.Fprintln(os.Stderr, "probeseed:", err)
		os.Exit(1)
	}
}

func run(out string, list nets) error {
	table := asndb.Table{Built: time.Now().UTC()}
	seen := map[uint32]bool{}
	for _, item := range list {
		p := strings.SplitN(item, "=", 4)
		if len(p) != 4 {
			return fmt.Errorf("%q is not CIDR=ASN=COUNTRY=NAME", item)
		}
		prefix, err := netip.ParsePrefix(p[0])
		if err != nil || !prefix.Addr().Is4() {
			return fmt.Errorf("%q: not an IPv4 prefix", p[0])
		}
		asn, err := strconv.ParseUint(p[1], 10, 32)
		if err != nil || asn == 0 {
			return fmt.Errorf("%q: not an ASN", p[1])
		}
		prefix = prefix.Masked()
		start := be32(prefix.Addr().As4())
		span := uint32(1) << (32 - prefix.Bits())
		table.IPv4 = append(table.IPv4, asndb.IPv4Range{Start: start, End: start + span - 1, ASN: uint32(asn)})
		if !seen[uint32(asn)] {
			seen[uint32(asn)] = true
			table.ASNs = append(table.ASNs, asndb.Operator{ASN: uint32(asn), Country: p[2], Name: p[3]})
		}
	}
	sort.Slice(table.IPv4, func(i, j int) bool { return table.IPv4[i].Start < table.IPv4[j].Start })
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if err := asndb.Write(f, table); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func be32(b [4]byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

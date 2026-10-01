package awgcfg

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// CheckCPS validates one custom packet (an I1..I5 value) and returns its size in bytes. The grammar is the
// intersection of what amneziawg-go, the kernel module and the clients accept: a
// chain of <b 0xHEX> <r N> <rc N> <rd N> <t> with nothing between the tags, N in 1..1000, hex non-empty and
// even, at most one <t>. <c> is refused (only the module knows it, clients reject the config) and so is '#'
// (the comment character of .conf files).
func CheckCPS(s string) (size int, err error) {
	if s == "" {
		return 0, nil
	}
	if strings.ContainsRune(s, '#') {
		return 0, errors.New("'#' is the comment character of .conf files")
	}
	ts := 0
	for rest := s; rest != ""; {
		if rest[0] != '<' {
			return 0, fmt.Errorf("unexpected %q outside a tag", snippet(rest))
		}
		end := strings.IndexByte(rest, '>')
		if end < 0 {
			return 0, errors.New("unterminated tag")
		}
		tag, rest2 := rest[1:end], rest[end+1:]
		rest = rest2
		name, arg, hasArg := strings.Cut(tag, " ")
		switch name {
		case "b":
			hex, ok := strings.CutPrefix(arg, "0x")
			if !hasArg || !ok || hex == "" || len(hex)%2 != 0 || !isHex(hex) {
				return 0, errors.New("<b> needs 0x followed by a non-empty, even-length hex string")
			}
			size += len(hex) / 2
		case "r", "rc", "rd":
			n, err := strconv.Atoi(arg)
			if !hasArg || err != nil || arg != strconv.Itoa(n) || n < 1 || n > 1000 {
				return 0, fmt.Errorf("<%s> needs a length in 1..1000", name)
			}
			size += n
		case "t":
			if hasArg {
				return 0, errors.New("<t> takes no argument")
			}
			if ts++; ts > 1 {
				return 0, errors.New("at most one <t> per packet")
			}
			size += 4
		case "c":
			return 0, errors.New("<c> is accepted only by the kernel module; clients reject the config")
		default:
			return 0, fmt.Errorf("unknown tag <%s>", snippet(name))
		}
	}
	return size, nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func snippet(s string) string {
	if len(s) > 12 {
		return s[:12] + "..."
	}
	return s
}

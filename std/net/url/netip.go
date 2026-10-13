package url

import (
	"github.com/eugenioenko/goalchemy/std/strconv"
	"github.com/eugenioenko/goalchemy/std/strings"
)

type parseAddrError struct {
	in  string
	msg string
	at  string
}

func (err parseAddrError) Error() string {
	q := strconv.Quote
	if err.at != "" {
		return "ParseAddr(" + q(err.in) + "): " + err.msg + " (at " + q(err.at) + ")"
	}
	return "ParseAddr(" + q(err.in) + "): " + err.msg
}

func parseAddr(s string) (is4 bool, err error) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.':
			return true, parseIPv4Fields(s, 0, len(s))
		case ':':
			return false, parseIPv6(s)
		case '%':
			return false, parseAddrError{in: s, msg: "missing IPv6 address"}
		}
	}
	return false, parseAddrError{in: s, msg: "unable to parse IP"}
}

func parseIPv4Fields(in string, off, end int) error {
	var val, pos int
	var digLen int
	s := in[off:end]
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if digLen == 1 && val == 0 {
				return parseAddrError{in: in, msg: "IPv4 field has octet with leading zero"}
			}
			val = val*10 + int(s[i]) - '0'
			digLen++
			if val > 255 {
				return parseAddrError{in: in, msg: "IPv4 field has value >255"}
			}
		} else if s[i] == '.' {
			if i == 0 || i == len(s)-1 || s[i-1] == '.' {
				return parseAddrError{in: in, msg: "IPv4 field must have at least one digit", at: s[i:]}
			}
			if pos == 3 {
				return parseAddrError{in: in, msg: "IPv4 address too long"}
			}
			pos++
			val = 0
			digLen = 0
		} else {
			return parseAddrError{in: in, msg: "unexpected character", at: s[i:]}
		}
	}
	if pos < 3 {
		return parseAddrError{in: in, msg: "IPv4 address too short"}
	}
	return nil
}

func parseIPv6(in string) error {
	s := in

	zone := ""
	i := strings.IndexByte(s, '%')
	if i != -1 {
		s, zone = s[:i], s[i+1:]
		if zone == "" {
			return parseAddrError{in: in, msg: "zone must be a non-empty string"}
		}
	}

	ellipsis := -1

	if len(s) >= 2 && s[0] == ':' && s[1] == ':' {
		ellipsis = 0
		s = s[2:]
		if len(s) == 0 {
			return nil
		}
	}

	i = 0
	for i < 16 {
		off := 0
		acc := uint32(0)
		for ; off < len(s); off++ {
			c := s[off]
			if c >= '0' && c <= '9' {
				acc = (acc << 4) + uint32(c-'0')
			} else if c >= 'a' && c <= 'f' {
				acc = (acc << 4) + uint32(c-'a'+10)
			} else if c >= 'A' && c <= 'F' {
				acc = (acc << 4) + uint32(c-'A'+10)
			} else {
				break
			}
			if off > 3 {
				return parseAddrError{in: in, msg: "each group must have 4 or less digits", at: s}
			}
			if acc > 0xffff {
				return parseAddrError{in: in, msg: "IPv6 field has value >=2^16", at: s}
			}
		}
		if off == 0 {
			return parseAddrError{in: in, msg: "each colon-separated field must have at least one digit", at: s}
		}

		if off < len(s) && s[off] == '.' {
			if ellipsis < 0 && i != 12 {
				return parseAddrError{in: in, msg: "embedded IPv4 address must replace the final 2 fields of the address", at: s}
			}
			if i+4 > 16 {
				return parseAddrError{in: in, msg: "too many hex fields to fit an embedded IPv4 at the end of the address", at: s}
			}

			end := len(in)
			if len(zone) > 0 {
				end -= len(zone) + 1
			}
			if err := parseIPv4Fields(in, end-len(s), end); err != nil {
				return err
			}
			s = ""
			i += 4
			break
		}

		i += 2

		s = s[off:]
		if len(s) == 0 {
			break
		}

		if s[0] != ':' {
			return parseAddrError{in: in, msg: "unexpected character, want colon", at: s}
		} else if len(s) == 1 {
			return parseAddrError{in: in, msg: "colon must be followed by more characters", at: s}
		}
		s = s[1:]

		if s[0] == ':' {
			if ellipsis >= 0 {
				return parseAddrError{in: in, msg: "multiple :: in address", at: s}
			}
			ellipsis = i
			s = s[1:]
			if len(s) == 0 {
				break
			}
		}
	}

	if len(s) != 0 {
		return parseAddrError{in: in, msg: "trailing garbage after address", at: s}
	}

	if i < 16 {
		if ellipsis < 0 {
			return parseAddrError{in: in, msg: "address string too short"}
		}
	} else if ellipsis >= 0 {
		return parseAddrError{in: in, msg: "the :: must expand to at least one field of zeros"}
	}
	return nil
}

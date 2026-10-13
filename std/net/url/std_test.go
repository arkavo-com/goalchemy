package url

import (
	"fmt"
	stdurl "net/url"
	"reflect"
	"testing"
)

var extraInputs = []string{
	"", "*", "http:", "http:/", "http://", "http:///", "//", "///x", "/", "a", "a:b", "./a:b", "a/b:c",
	"HTTP://Example.COM:8080/a/../b/./c?x=1&y=%zz#Frag%20ment", "https://kas.example.com", "https://kas.example.com:443/kas",
	"https://KAS.example.com:8443/kas/v2/rewrap?x=y#z", "http://[::1]", "http://[::1]:", "http://[::1]:80/p", "http://[::1]x",
	"http://[fe80::1%25en0]:8080/", "http://[fe80::1%25%65%6e0]/", "http://[fe80::1%en0]/", "http://[fe80::1%25]/",
	"http://[127.0.0.1]/", "http://[::ffff:127.0.0.1]/", "http://[1:2:3:4:5:6:7:8:9]/", "http://[1::2::3]/",
	"http://[12345::]/", "http://[::1.2.3]/", "http://[::1.2.3.4.5]/", "http://[1.2.3.04]/", "http://[1.2.3.256]/",
	"http://[:1]/", "http://[1:]/", "http://[1:2:3:4:5:6:7::]/", "http://[1:2:3:4:5:6:7:8::]/", "http://[g::]/",
	"http://[::1", "http://::1]/", "http://a[::1]/", "http://[::1]:8a/", "http://[%25]/", "http://[]/",
	"http://host:port", "http://host:", "http://host:80:", "http://1:2:3:4/", "mongodb://h1:1,h2:2/db",
	"postgres://h1:1,h2:2/db", "http://user:pass@host/", "http://user@host/", "http://us%zzer@host/",
	"http://us er@host/", "http://user:p@ss@host/", "http://%41:%42@host/", "http://h%20st/", "http://h%41st/",
	"http://h%c3%a9st/", "http://héllo/", "http://h<st>/", "http://host/%2f/%2F/a%20b?q=a+b#f%2f",
	"http://host/a?b?c", "http://host/a?", "http://host/a??", "http://host/a#", "http://host/a#b#c", "?", "#",
	"?a=1#b", "mailto:user@example.com", "mailto:?subject=x", "urn:ietf:rfc:3986", "file:///etc/passwd",
	"file://localhost/etc", "http://host\x00/", "http://host/\x7f", "\thttp://host", "1http://host", "+a:b",
	"a.b-c+d:rest", "-:x", ":x", "http://host/a%", "http://host/a%2", "http://host/a%g0", "http://host/%E2%82%AC",
	"http://host/€", "http://host/ space", "http://host/a;b,c=d", "http://host//double//slash",
	"https://example.com/grpc.health.v1.Health/Check", "https://idp.example.com/realms/x/.well-known/openid-configuration",
	"https:/example.com", "https:example.com", "https://example.com:0", "https://example.com:65536",
	"https://example.com:-1", "https://[::]", "https://[::]:443", "ws://host:8080/socket?x", "HTTPS://HOST",
}

func parseInputs() []string {
	var in []string
	for _, tt := range urltests {
		in = append(in, tt.in)
	}
	for _, tt := range parseRequestURLTests {
		in = append(in, tt.url)
	}
	for _, tt := range stringURLTests {
		in = append(in, tt.want)
	}
	for _, tt := range resolveReferenceTests {
		in = append(in, tt.base, tt.rel, tt.expected)
	}
	for _, tt := range requritests {
		in = append(in, tt.url.String(), tt.out)
	}
	return append(in, extraInputs...)
}

func describe(u *URL, err error) string {
	if err != nil {
		e := err.(*Error)
		return fmt.Sprintf("err %T %s|%s|%v|%T", err, e.Op, e.URL, err, e.Err)
	}
	return fmt.Sprintf("%q %q %q %v %q %q %q %q %q %q %v %v | %q %q %q %q %q %v %q %q %q",
		u.Scheme, u.Opaque, u.User.String(), u.User != nil, u.Host, u.Path, u.RawPath, u.RawQuery, u.Fragment,
		u.RawFragment, u.ForceQuery, u.OmitHost, u.String(), u.Hostname(), u.Port(), u.EscapedPath(),
		u.EscapedFragment(), u.IsAbs(), u.RequestURI(), u.Redacted(), u.Query().Encode())
}

func describeStd(u *stdurl.URL, err error) string {
	if err != nil {
		e := err.(*stdurl.Error)
		return fmt.Sprintf("err %T %s|%s|%v|%T", err, e.Op, e.URL, err, e.Err)
	}
	return fmt.Sprintf("%q %q %q %v %q %q %q %q %q %q %v %v | %q %q %q %q %q %v %q %q %q",
		u.Scheme, u.Opaque, u.User.String(), u.User != nil, u.Host, u.Path, u.RawPath, u.RawQuery, u.Fragment,
		u.RawFragment, u.ForceQuery, u.OmitHost, u.String(), u.Hostname(), u.Port(), u.EscapedPath(),
		u.EscapedFragment(), u.IsAbs(), u.RequestURI(), u.Redacted(), u.Query().Encode())
}

func TestParseMatchesStd(t *testing.T) {
	for _, s := range parseInputs() {
		got := describe(Parse(s))
		want := describeStd(stdurl.Parse(s))
		if !sameDescription(got, want) {
			t.Errorf("Parse(%q)\n got %s\nwant %s", s, got, want)
		}
		got = describe(ParseRequestURI(s))
		want = describeStd(stdurl.ParseRequestURI(s))
		if !sameDescription(got, want) {
			t.Errorf("ParseRequestURI(%q)\n got %s\nwant %s", s, got, want)
		}
	}
}

func sameDescription(got, want string) bool {
	return stripErrTypes(got) == stripErrTypes(want)
}

func stripErrTypes(s string) string {
	if len(s) < 4 || s[:4] != "err " {
		return s
	}
	i := len(s) - 1
	for i >= 0 && s[i] != '|' {
		i--
	}
	return s[:i]
}

func TestResolveMatchesStd(t *testing.T) {
	in := parseInputs()
	for _, b := range in {
		base, err := Parse(b)
		sbase, serr := stdurl.Parse(b)
		if (err == nil) != (serr == nil) {
			t.Fatalf("Parse(%q) error mismatch: %v vs %v", b, err, serr)
		}
		if err != nil {
			continue
		}
		for _, r := range in {
			got, err := base.Parse(r)
			want, serr := sbase.Parse(r)
			if g, w := describe(got, err), describeStd(want, serr); !sameDescription(g, w) {
				t.Fatalf("Parse(%q).Parse(%q)\n got %s\nwant %s", b, r, g, w)
			}
		}
	}
}

var joinElems = [][]string{
	nil, {""}, {"a"}, {"a", "b"}, {"/a/", "/b/"}, {"..", "x"}, {"../../.."}, {"./a"}, {"a/"}, {"%2F"}, {"100%"},
	{"grpc.health.v1.Health", "Check"}, {".well-known/openid-configuration"}, {"a b"}, {"€"}, {"x?y"}, {"x#y"},
}

func TestJoinPathMatchesStd(t *testing.T) {
	for _, b := range parseInputs() {
		for _, e := range joinElems {
			got, err := JoinPath(b, e...)
			want, serr := stdurl.JoinPath(b, e...)
			if got != want || fmt.Sprint(err) != fmt.Sprint(serr) {
				t.Fatalf("JoinPath(%q, %q) = %q, %v; want %q, %v", b, e, got, err, want, serr)
			}
			u, err := Parse(b)
			su, _ := stdurl.Parse(b)
			if err != nil {
				continue
			}
			if g, w := describe(u.JoinPath(e...), nil), describeStd(su.JoinPath(e...), nil); g != w {
				t.Fatalf("Parse(%q).JoinPath(%q)\n got %s\nwant %s", b, e, g, w)
			}
		}
	}
}

func escapeInputs() []string {
	in := []string{"", " ", "+", "%", "%%", "%2", "%zz", "%25", "%2B", "a+b c", "€", "\xff", "\x00", "a/b?c#d&e=f;g,h"}
	for i := 0; i < 256; i++ {
		in = append(in, string([]byte{byte(i)}), "%"+fmt.Sprintf("%02X", i), "%"+fmt.Sprintf("%02x", i))
	}
	for _, tt := range unescapeTests {
		in = append(in, tt.in, tt.out)
	}
	for _, tt := range queryEscapeTests {
		in = append(in, tt.in, tt.out)
	}
	for _, tt := range pathEscapeTests {
		in = append(in, tt.in, tt.out)
	}
	return in
}

func TestEscapingMatchesStd(t *testing.T) {
	for _, s := range escapeInputs() {
		if got, want := QueryEscape(s), stdurl.QueryEscape(s); got != want {
			t.Errorf("QueryEscape(%q) = %q, want %q", s, got, want)
		}
		if got, want := PathEscape(s), stdurl.PathEscape(s); got != want {
			t.Errorf("PathEscape(%q) = %q, want %q", s, got, want)
		}
		got, err := QueryUnescape(s)
		want, serr := stdurl.QueryUnescape(s)
		if got != want || fmt.Sprint(err) != fmt.Sprint(serr) {
			t.Errorf("QueryUnescape(%q) = %q, %v; want %q, %v", s, got, err, want, serr)
		}
		got, err = PathUnescape(s)
		want, serr = stdurl.PathUnescape(s)
		if got != want || fmt.Sprint(err) != fmt.Sprint(serr) {
			t.Errorf("PathUnescape(%q) = %q, %v; want %q, %v", s, got, err, want, serr)
		}
		v, err := ParseQuery(s)
		sv, serr := stdurl.ParseQuery(s)
		if !reflect.DeepEqual(map[string][]string(v), map[string][]string(sv)) || fmt.Sprint(err) != fmt.Sprint(serr) {
			t.Errorf("ParseQuery(%q) = %v, %v; want %v, %v", s, v, err, sv, serr)
		}
		if got, want := v.Encode(), sv.Encode(); got != want {
			t.Errorf("ParseQuery(%q).Encode() = %q, want %q", s, got, want)
		}
	}
}

func TestErrorMatchesStd(t *testing.T) {
	errs := []error{nil, EscapeError("%zz"), InvalidHostError("<")}
	serrs := []error{nil, stdurl.EscapeError("%zz"), stdurl.InvalidHostError("<")}
	for i := range errs {
		got := (&Error{"parse", "a\"b\n", errs[i]}).Error()
		want := (&stdurl.Error{Op: "parse", URL: "a\"b\n", Err: serrs[i]}).Error()
		if got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	}
}

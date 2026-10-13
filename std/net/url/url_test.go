package url

import (
	"bytes"
	encodingPkg "encoding"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type URLTest struct {
	in        string
	out       *URL
	roundtrip string
}

var urltests = []URLTest{
	{
		"http://www.google.com",
		&URL{
			Scheme: "http",
			Host:   "www.google.com",
		},
		"",
	},

	{
		"http://www.google.com/",
		&URL{
			Scheme: "http",
			Host:   "www.google.com",
			Path:   "/",
		},
		"",
	},

	{
		"http://www.google.com/file%20one%26two",
		&URL{
			Scheme:  "http",
			Host:    "www.google.com",
			Path:    "/file one&two",
			RawPath: "/file%20one%26two",
		},
		"",
	},

	{
		"http://www.google.com/#file%20one%26two",
		&URL{
			Scheme:      "http",
			Host:        "www.google.com",
			Path:        "/",
			Fragment:    "file one&two",
			RawFragment: "file%20one%26two",
		},
		"",
	},

	{
		"ftp://webmaster@www.google.com/",
		&URL{
			Scheme: "ftp",
			User:   User("webmaster"),
			Host:   "www.google.com",
			Path:   "/",
		},
		"",
	},

	{
		"ftp://john%20doe@www.google.com/",
		&URL{
			Scheme: "ftp",
			User:   User("john doe"),
			Host:   "www.google.com",
			Path:   "/",
		},
		"ftp://john%20doe@www.google.com/",
	},

	{
		"http://www.google.com/?",
		&URL{
			Scheme:     "http",
			Host:       "www.google.com",
			Path:       "/",
			ForceQuery: true,
		},
		"",
	},

	{
		"http://www.google.com/?foo=bar?",
		&URL{
			Scheme:   "http",
			Host:     "www.google.com",
			Path:     "/",
			RawQuery: "foo=bar?",
		},
		"",
	},

	{
		"http://www.google.com/?q=go+language",
		&URL{
			Scheme:   "http",
			Host:     "www.google.com",
			Path:     "/",
			RawQuery: "q=go+language",
		},
		"",
	},

	{
		"http://www.google.com/?q=go%20language",
		&URL{
			Scheme:   "http",
			Host:     "www.google.com",
			Path:     "/",
			RawQuery: "q=go%20language",
		},
		"",
	},

	{
		"http://www.google.com/a%20b?q=c+d",
		&URL{
			Scheme:   "http",
			Host:     "www.google.com",
			Path:     "/a b",
			RawQuery: "q=c+d",
		},
		"",
	},

	{
		"http:www.google.com/?q=go+language",
		&URL{
			Scheme:   "http",
			Opaque:   "www.google.com/",
			RawQuery: "q=go+language",
		},
		"http:www.google.com/?q=go+language",
	},

	{
		"http:%2f%2fwww.google.com/?q=go+language",
		&URL{
			Scheme:   "http",
			Opaque:   "%2f%2fwww.google.com/",
			RawQuery: "q=go+language",
		},
		"http:%2f%2fwww.google.com/?q=go+language",
	},

	{
		"mailto:/webmaster@golang.org",
		&URL{
			Scheme:   "mailto",
			Path:     "/webmaster@golang.org",
			OmitHost: true,
		},
		"",
	},

	{
		"mailto:webmaster@golang.org",
		&URL{
			Scheme: "mailto",
			Opaque: "webmaster@golang.org",
		},
		"",
	},

	{
		"/foo?query=http://bad",
		&URL{
			Path:     "/foo",
			RawQuery: "query=http://bad",
		},
		"",
	},

	{
		"//foo",
		&URL{
			Host: "foo",
		},
		"",
	},

	{
		"//user@foo/path?a=b",
		&URL{
			User:     User("user"),
			Host:     "foo",
			Path:     "/path",
			RawQuery: "a=b",
		},
		"",
	},

	{
		"///threeslashes",
		&URL{
			Path: "///threeslashes",
		},
		"",
	},
	{
		"http://user:password@google.com",
		&URL{
			Scheme: "http",
			User:   UserPassword("user", "password"),
			Host:   "google.com",
		},
		"http://user:password@google.com",
	},

	{
		"http://j@ne:password@google.com",
		&URL{
			Scheme: "http",
			User:   UserPassword("j@ne", "password"),
			Host:   "google.com",
		},
		"http://j%40ne:password@google.com",
	},

	{
		"http://jane:p@ssword@google.com",
		&URL{
			Scheme: "http",
			User:   UserPassword("jane", "p@ssword"),
			Host:   "google.com",
		},
		"http://jane:p%40ssword@google.com",
	},
	{
		"http://j@ne:password@google.com/p@th?q=@go",
		&URL{
			Scheme:   "http",
			User:     UserPassword("j@ne", "password"),
			Host:     "google.com",
			Path:     "/p@th",
			RawQuery: "q=@go",
		},
		"http://j%40ne:password@google.com/p@th?q=@go",
	},
	{
		"http://www.google.com/?q=go+language#foo",
		&URL{
			Scheme:   "http",
			Host:     "www.google.com",
			Path:     "/",
			RawQuery: "q=go+language",
			Fragment: "foo",
		},
		"",
	},
	{
		"http://www.google.com/?q=go+language#foo&bar",
		&URL{
			Scheme:   "http",
			Host:     "www.google.com",
			Path:     "/",
			RawQuery: "q=go+language",
			Fragment: "foo&bar",
		},
		"http://www.google.com/?q=go+language#foo&bar",
	},
	{
		"http://www.google.com/?q=go+language#foo%26bar",
		&URL{
			Scheme:      "http",
			Host:        "www.google.com",
			Path:        "/",
			RawQuery:    "q=go+language",
			Fragment:    "foo&bar",
			RawFragment: "foo%26bar",
		},
		"http://www.google.com/?q=go+language#foo%26bar",
	},
	{
		"file:///home/adg/rabbits",
		&URL{
			Scheme: "file",
			Host:   "",
			Path:   "/home/adg/rabbits",
		},
		"file:///home/adg/rabbits",
	},

	{
		"file:///C:/FooBar/Baz.txt",
		&URL{
			Scheme: "file",
			Host:   "",
			Path:   "/C:/FooBar/Baz.txt",
		},
		"file:///C:/FooBar/Baz.txt",
	},

	{
		"MaIlTo:webmaster@golang.org",
		&URL{
			Scheme: "mailto",
			Opaque: "webmaster@golang.org",
		},
		"mailto:webmaster@golang.org",
	},

	{
		"a/b/c",
		&URL{
			Path: "a/b/c",
		},
		"a/b/c",
	},

	{
		"http://%3Fam:pa%3Fsword@google.com",
		&URL{
			Scheme: "http",
			User:   UserPassword("?am", "pa?sword"),
			Host:   "google.com",
		},
		"",
	},

	{
		"http://192.168.0.1/",
		&URL{
			Scheme: "http",
			Host:   "192.168.0.1",
			Path:   "/",
		},
		"",
	},

	{
		"http://192.168.0.1:8080/",
		&URL{
			Scheme: "http",
			Host:   "192.168.0.1:8080",
			Path:   "/",
		},
		"",
	},

	{
		"http://[fe80::1]/",
		&URL{
			Scheme: "http",
			Host:   "[fe80::1]",
			Path:   "/",
		},
		"",
	},

	{
		"http://[fe80::1]:8080/",
		&URL{
			Scheme: "http",
			Host:   "[fe80::1]:8080",
			Path:   "/",
		},
		"",
	},

	{
		"https://[2001:db8::1]:8443/test/path",
		&URL{
			Scheme: "https",
			Host:   "[2001:db8::1]:8443",
			Path:   "/test/path",
		},
		"",
	},

	{
		"http://[fe80::1%25en0]/",
		&URL{
			Scheme: "http",
			Host:   "[fe80::1%en0]",
			Path:   "/",
		},
		"",
	},

	{
		"http://[fe80::1%25en0]:8080/",
		&URL{
			Scheme: "http",
			Host:   "[fe80::1%en0]:8080",
			Path:   "/",
		},
		"",
	},

	{
		"http://[fe80::1%25%65%6e%301-._~]/",
		&URL{
			Scheme: "http",
			Host:   "[fe80::1%en01-._~]",
			Path:   "/",
		},
		"http://[fe80::1%25en01-._~]/",
	},

	{
		"http://[fe80::1%25%65%6e%301-._~]:8080/",
		&URL{
			Scheme: "http",
			Host:   "[fe80::1%en01-._~]:8080",
			Path:   "/",
		},
		"http://[fe80::1%25en01-._~]:8080/",
	},

	{
		"http://rest.rsc.io/foo%2fbar/baz%2Fquux?alt=media",
		&URL{
			Scheme:   "http",
			Host:     "rest.rsc.io",
			Path:     "/foo/bar/baz/quux",
			RawPath:  "/foo%2fbar/baz%2Fquux",
			RawQuery: "alt=media",
		},
		"",
	},

	{
		"mysql://a,b,c/bar",
		&URL{
			Scheme: "mysql",
			Host:   "a,b,c",
			Path:   "/bar",
		},
		"",
	},

	{
		"scheme://!$&'()*+,;=hello!:1/path",
		&URL{
			Scheme: "scheme",
			Host:   "!$&'()*+,;=hello!:1",
			Path:   "/path",
		},
		"",
	},

	{
		"http://host/!$&'()*+,;=:@[hello]",
		&URL{
			Scheme:  "http",
			Host:    "host",
			Path:    "/!$&'()*+,;=:@[hello]",
			RawPath: "/!$&'()*+,;=:@[hello]",
		},
		"",
	},

	{
		"http://example.com/oid/[order_id]",
		&URL{
			Scheme:  "http",
			Host:    "example.com",
			Path:    "/oid/[order_id]",
			RawPath: "/oid/[order_id]",
		},
		"",
	},

	{
		"http://192.168.0.2:8080/foo",
		&URL{
			Scheme: "http",
			Host:   "192.168.0.2:8080",
			Path:   "/foo",
		},
		"",
	},
	{
		"http://192.168.0.2:/foo",
		&URL{
			Scheme: "http",
			Host:   "192.168.0.2:",
			Path:   "/foo",
		},
		"",
	},
	{
		"http://[2b01:e34:ef40:7730:8e70:5aff:fefe:edac]:8080/foo",
		&URL{
			Scheme: "http",
			Host:   "[2b01:e34:ef40:7730:8e70:5aff:fefe:edac]:8080",
			Path:   "/foo",
		},
		"",
	},
	{
		"http://[2b01:e34:ef40:7730:8e70:5aff:fefe:edac]:/foo",
		&URL{
			Scheme: "http",
			Host:   "[2b01:e34:ef40:7730:8e70:5aff:fefe:edac]:",
			Path:   "/foo",
		},
		"",
	},

	{
		"http://hello.世界.com/foo",
		&URL{
			Scheme: "http",
			Host:   "hello.世界.com",
			Path:   "/foo",
		},
		"http://hello.%E4%B8%96%E7%95%8C.com/foo",
	},
	{
		"http://hello.%e4%b8%96%e7%95%8c.com/foo",
		&URL{
			Scheme: "http",
			Host:   "hello.世界.com",
			Path:   "/foo",
		},
		"http://hello.%E4%B8%96%E7%95%8C.com/foo",
	},
	{
		"http://hello.%E4%B8%96%E7%95%8C.com/foo",
		&URL{
			Scheme: "http",
			Host:   "hello.世界.com",
			Path:   "/foo",
		},
		"",
	},

	{
		"http://example.com//foo",
		&URL{
			Scheme: "http",
			Host:   "example.com",
			Path:   "//foo",
		},
		"",
	},

	{
		"myscheme://authority<\"hi\">/foo",
		&URL{
			Scheme: "myscheme",
			Host:   "authority<\"hi\">",
			Path:   "/foo",
		},
		"",
	},

	{
		"tcp://[2020::2020:20:2020:2020%25Windows%20Loves%20Spaces]:2020",
		&URL{
			Scheme: "tcp",
			Host:   "[2020::2020:20:2020:2020%Windows Loves Spaces]:2020",
		},
		"",
	},

	{
		"magnet:?xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a&dn",
		&URL{
			Scheme:   "magnet",
			Host:     "",
			Path:     "",
			RawQuery: "xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a&dn",
		},
		"magnet:?xt=urn:btih:c12fe1c06bba254a9dc9f519b335aa7c1367a88a&dn",
	},
	{
		"mailto:?subject=hi",
		&URL{
			Scheme:   "mailto",
			Host:     "",
			Path:     "",
			RawQuery: "subject=hi",
		},
		"mailto:?subject=hi",
	},

	{
		"postgres://host1:1,host2:2,host3:3",
		&URL{
			Scheme: "postgres",
			Host:   "host1:1,host2:2,host3:3",
			Path:   "",
		},
		"postgres://host1:1,host2:2,host3:3",
	},
	{
		"postgresql://host1:1,host2:2,host3:3",
		&URL{
			Scheme: "postgresql",
			Host:   "host1:1,host2:2,host3:3",
			Path:   "",
		},
		"postgresql://host1:1,host2:2,host3:3",
	},

	{
		"mongodb://user:password@host1:1,host2:2,host3:3",
		&URL{
			Scheme: "mongodb",
			User:   UserPassword("user", "password"),
			Host:   "host1:1,host2:2,host3:3",
			Path:   "",
		},
		"",
	},
	{
		"mongodb+srv://user:password@host1:1,host2:2,host3:3",
		&URL{
			Scheme: "mongodb+srv",
			User:   UserPassword("user", "password"),
			Host:   "host1:1,host2:2,host3:3",
			Path:   "",
		},
		"",
	},

	{
		"",
		&URL{
			Scheme:   "http",
			OmitHost: true,
			Path:     "//host/path",
		},
		"http:%2F/host/path",
	},
}

func ufmt(u *URL) string {
	var user, pass any
	if u.User != nil {
		user = u.User.Username()
		if p, ok := u.User.Password(); ok {
			pass = p
		}
	}
	return fmt.Sprintf("opaque=%q, scheme=%q, user=%#v, pass=%#v, host=%q, path=%q, rawpath=%q, rawq=%q, frag=%q, rawfrag=%q, forcequery=%v, omithost=%t",
		u.Opaque, u.Scheme, user, pass, u.Host, u.Path, u.RawPath, u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery, u.OmitHost)
}

func BenchmarkString(b *testing.B) {
	b.StopTimer()
	b.ReportAllocs()
	for _, tt := range urltests {
		if tt.in == "" {
			continue
		}
		u, err := Parse(tt.in)
		if err != nil {
			b.Errorf("Parse(%q) returned error %s", tt.in, err)
			continue
		}
		if tt.roundtrip == "" {
			continue
		}
		b.StartTimer()
		var g string
		for i := 0; i < b.N; i++ {
			g = u.String()
		}
		b.StopTimer()
		if w := tt.roundtrip; b.N > 0 && g != w {
			b.Errorf("Parse(%q).String() == %q, want %q", tt.in, g, w)
		}
	}
}

func TestParse(t *testing.T) {
	for _, tt := range urltests {
		if tt.in == "" {
			continue
		}
		u, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q) returned error %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(u, tt.out) {
			t.Errorf("Parse(%q):\n\tgot  %v\n\twant %v\n", tt.in, ufmt(u), ufmt(tt.out))
		}
	}
}

const pathThatLooksSchemeRelative = "//not.a.user@not.a.host/just/a/path"

var parseRequestURLTests = []struct {
	url           string
	expectedValid bool
}{
	{"http://foo.com", true},
	{"http://foo.com/", true},
	{"http://foo.com/path", true},
	{"/", true},
	{pathThatLooksSchemeRelative, true},
	{"//not.a.user@%66%6f%6f.com/just/a/path/also", true},
	{"*", true},
	{"http://192.168.0.1/", true},
	{"http://192.168.0.1:8080/", true},
	{"http://[fe80::1]/", true},
	{"http://[fe80::1]:8080/", true},

	{"http://[fe80::1%25en0]/", true},
	{"http://[fe80::1%25en0]:8080/", true},
	{"http://[fe80::1%25%65%6e%301-._~]/", true},
	{"http://[fe80::1%25%65%6e%301-._~]:8080/", true},

	{"foo.html", false},
	{"../dir/", false},
	{" http://foo.com", false},
	{"http://192.168.0.%31/", false},
	{"http://192.168.0.%31:8080/", false},
	{"http://[fe80::%31]/", false},
	{"http://[fe80::%31]:8080/", false},
	{"http://[fe80::%31%25en0]/", false},
	{"http://[fe80::%31%25en0]:8080/", false},

	{"http://[fe80::1%en0]/", false},
	{"http://[fe80::1%en0]:8080/", false},

	{"https://[1:2:3:4:5:6:7:8]", true},
	{"https://[2001:db8::a:b:c:d]", true},
	{"https://[fe80::1%25eth0]", true},
	{"https://[fe80::abc:def%254]", true},
	{"https://[2001:db8::1]/path", true},
	{"https://[fe80::1%25eth0]/path?query=1", true},

	{"https://[::ffff:192.0.2.1]", true},
	{"https://[:1] ", false},
	{"https://[1:2:3:4:5:6:7:8:9]", false},
	{"https://[1::1::1]", false},
	{"https://[1:2:3:]", false},
	{"https://[ffff::127.0.0.4000]", false},
	{"https://[0:0::test.com]:80", false},
	{"https://[2001:db8::test.com]", false},
	{"https://[test.com]", false},
	{"https://1:2:3:4:5:6:7:8", true},
	{"https://1:2:3:4:5:6:7:8:80", true},
	{"https://example.com:80:", true},
}

func TestParseRequestURI(t *testing.T) {
	for _, test := range parseRequestURLTests {
		_, err := ParseRequestURI(test.url)
		if test.expectedValid && err != nil {
			t.Errorf("ParseRequestURI(%q) gave err %v; want no error", test.url, err)
		} else if !test.expectedValid && err == nil {
			t.Errorf("ParseRequestURI(%q) gave nil error; want some error", test.url)
		}
	}

	url, err := ParseRequestURI(pathThatLooksSchemeRelative)
	if err != nil {
		t.Fatalf("Unexpected error %v", err)
	}
	if url.Path != pathThatLooksSchemeRelative {
		t.Errorf("ParseRequestURI path:\ngot  %q\nwant %q", url.Path, pathThatLooksSchemeRelative)
	}
}

var stringURLTests = []struct {
	url  URL
	want string
}{
	{
		url: URL{
			Scheme: "http",
			Host:   "www.google.com",
			Path:   "search",
		},
		want: "http://www.google.com/search",
	},

	{
		url: URL{
			Path: "this:that",
		},
		want: "./this:that",
	},

	{
		url: URL{
			Path: "here/this:that",
		},
		want: "here/this:that",
	},

	{
		url: URL{
			Scheme: "http",
			Host:   "www.google.com",
			Path:   "this:that",
		},
		want: "http://www.google.com/this:that",
	},
}

func TestURLString(t *testing.T) {
	for _, tt := range urltests {
		u := tt.out
		if tt.in != "" {
			var err error
			u, err = Parse(tt.in)
			if err != nil {
				t.Errorf("Parse(%q) returned error %s", tt.in, err)
				continue
			}
		}
		expected := tt.in
		if tt.roundtrip != "" {
			expected = tt.roundtrip
		}
		s := u.String()
		if s != expected {
			t.Errorf("Parse(%q).String() == %q (expected %q)", tt.in, s, expected)
		}
	}

	for _, tt := range stringURLTests {
		if got := tt.url.String(); got != tt.want {
			t.Errorf("%+v.String() = %q; want %q", tt.url, got, tt.want)
		}
	}
}

func TestURLRedacted(t *testing.T) {
	cases := []struct {
		name string
		url  *URL
		want string
	}{
		{
			name: "non-blank Password",
			url: &URL{
				Scheme: "http",
				Host:   "host.tld",
				Path:   "this:that",
				User:   UserPassword("user", "password"),
			},
			want: "http://user:xxxxx@host.tld/this:that",
		},
		{
			name: "blank Password",
			url: &URL{
				Scheme: "http",
				Host:   "host.tld",
				Path:   "this:that",
				User:   User("user"),
			},
			want: "http://user@host.tld/this:that",
		},
		{
			name: "nil User",
			url: &URL{
				Scheme: "http",
				Host:   "host.tld",
				Path:   "this:that",
				User:   UserPassword("", "password"),
			},
			want: "http://:xxxxx@host.tld/this:that",
		},
		{
			name: "blank Username, blank Password",
			url: &URL{
				Scheme: "http",
				Host:   "host.tld",
				Path:   "this:that",
			},
			want: "http://host.tld/this:that",
		},
		{
			name: "empty URL",
			url:  &URL{},
			want: "",
		},
		{
			name: "nil URL",
			url:  nil,
			want: "",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if g, w := tt.url.Redacted(), tt.want; g != w {
				t.Fatalf("got: %q\nwant: %q", g, w)
			}
		})
	}
}

type EscapeTest struct {
	in  string
	out string
	err error
}

var unescapeTests = []EscapeTest{
	{
		"",
		"",
		nil,
	},
	{
		"abc",
		"abc",
		nil,
	},
	{
		"1%41",
		"1A",
		nil,
	},
	{
		"1%41%42%43",
		"1ABC",
		nil,
	},
	{
		"%4a",
		"J",
		nil,
	},
	{
		"%6F",
		"o",
		nil,
	},
	{
		"%",
		"",
		EscapeError("%"),
	},
	{
		"%a",
		"",
		EscapeError("%a"),
	},
	{
		"%1",
		"",
		EscapeError("%1"),
	},
	{
		"123%45%6",
		"",
		EscapeError("%6"),
	},
	{
		"%zzzzz",
		"",
		EscapeError("%zz"),
	},
	{
		"a+b",
		"a b",
		nil,
	},
	{
		"a%20b",
		"a b",
		nil,
	},
}

func TestUnescape(t *testing.T) {
	for _, tt := range unescapeTests {
		actual, err := QueryUnescape(tt.in)
		if actual != tt.out || (err != nil) != (tt.err != nil) {
			t.Errorf("QueryUnescape(%q) = %q, %s; want %q, %s", tt.in, actual, err, tt.out, tt.err)
		}

		in := tt.in
		out := tt.out
		if strings.Contains(tt.in, "+") {
			in = strings.ReplaceAll(tt.in, "+", "%20")
			actual, err := PathUnescape(in)
			if actual != tt.out || (err != nil) != (tt.err != nil) {
				t.Errorf("PathUnescape(%q) = %q, %s; want %q, %s", in, actual, err, tt.out, tt.err)
			}
			if tt.err == nil {
				s, err := QueryUnescape(strings.ReplaceAll(tt.in, "+", "XXX"))
				if err != nil {
					continue
				}
				in = tt.in
				out = strings.ReplaceAll(s, "XXX", "+")
			}
		}

		actual, err = PathUnescape(in)
		if actual != out || (err != nil) != (tt.err != nil) {
			t.Errorf("PathUnescape(%q) = %q, %s; want %q, %s", in, actual, err, out, tt.err)
		}
	}
}

var queryEscapeTests = []EscapeTest{
	{
		"",
		"",
		nil,
	},
	{
		"abc",
		"abc",
		nil,
	},
	{
		"one two",
		"one+two",
		nil,
	},
	{
		"10%",
		"10%25",
		nil,
	},
	{
		" ?&=#+%!<>#\"{}|\\^[]`☺\t:/@$'()*,;",
		"+%3F%26%3D%23%2B%25%21%3C%3E%23%22%7B%7D%7C%5C%5E%5B%5D%60%E2%98%BA%09%3A%2F%40%24%27%28%29%2A%2C%3B",
		nil,
	},
}

func TestQueryEscape(t *testing.T) {
	for _, tt := range queryEscapeTests {
		actual := QueryEscape(tt.in)
		if tt.out != actual {
			t.Errorf("QueryEscape(%q) = %q, want %q", tt.in, actual, tt.out)
		}

		roundtrip, err := QueryUnescape(actual)
		if roundtrip != tt.in || err != nil {
			t.Errorf("QueryUnescape(%q) = %q, %s; want %q, %s", actual, roundtrip, err, tt.in, "[no error]")
		}
	}
}

var pathEscapeTests = []EscapeTest{
	{
		"",
		"",
		nil,
	},
	{
		"abc",
		"abc",
		nil,
	},
	{
		"abc+def",
		"abc+def",
		nil,
	},
	{
		"a/b",
		"a%2Fb",
		nil,
	},
	{
		"one two",
		"one%20two",
		nil,
	},
	{
		"10%",
		"10%25",
		nil,
	},
	{
		" ?&=#+%!<>#\"{}|\\^[]`☺\t:/@$'()*,;",
		"%20%3F&=%23+%25%21%3C%3E%23%22%7B%7D%7C%5C%5E%5B%5D%60%E2%98%BA%09:%2F@$%27%28%29%2A%2C%3B",
		nil,
	},
}

func TestPathEscape(t *testing.T) {
	for _, tt := range pathEscapeTests {
		actual := PathEscape(tt.in)
		if tt.out != actual {
			t.Errorf("PathEscape(%q) = %q, want %q", tt.in, actual, tt.out)
		}

		roundtrip, err := PathUnescape(actual)
		if roundtrip != tt.in || err != nil {
			t.Errorf("PathUnescape(%q) = %q, %s; want %q, %s", actual, roundtrip, err, tt.in, "[no error]")
		}
	}
}

type EncodeQueryTest struct {
	m        Values
	expected string
}

var encodeQueryTests = []EncodeQueryTest{
	{nil, ""},
	{Values{}, ""},
	{Values{"q": {"puppies"}, "oe": {"utf8"}}, "oe=utf8&q=puppies"},
	{Values{"q": {"dogs", "&", "7"}}, "q=dogs&q=%26&q=7"},
	{Values{
		"a": {"a1", "a2", "a3"},
		"b": {"b1", "b2", "b3"},
		"c": {"c1", "c2", "c3"},
	}, "a=a1&a=a2&a=a3&b=b1&b=b2&b=b3&c=c1&c=c2&c=c3"},
	{Values{
		"a": {"a"},
		"b": {"b"},
		"c": {"c"},
		"d": {"d"},
		"e": {"e"},
		"f": {"f"},
		"g": {"g"},
		"h": {"h"},
		"i": {"i"},
	}, "a=a&b=b&c=c&d=d&e=e&f=f&g=g&h=h&i=i"},
}

func TestEncodeQuery(t *testing.T) {
	for _, tt := range encodeQueryTests {
		if q := tt.m.Encode(); q != tt.expected {
			t.Errorf(`EncodeQuery(%+v) = %q, want %q`, tt.m, q, tt.expected)
		}
	}
}

func BenchmarkEncodeQuery(b *testing.B) {
	for _, tt := range encodeQueryTests {
		b.Run(tt.expected, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				tt.m.Encode()
			}
		})
	}
}

var resolvePathTests = []struct {
	base, ref, expected string
}{
	{"a/b", ".", "/a/"},
	{"a/b", "c", "/a/c"},
	{"a/b", "..", "/"},
	{"a/", "..", "/"},
	{"a/", "../..", "/"},
	{"a/b/c", "..", "/a/"},
	{"a/b/c", "../d", "/a/d"},
	{"a/b/c", ".././d", "/a/d"},
	{"a/b", "./..", "/"},
	{"a/./b", ".", "/a/"},
	{"a/../", ".", "/"},
	{"a/.././b", "c", "/c"},
}

func TestResolvePath(t *testing.T) {
	for _, test := range resolvePathTests {
		got := resolvePath(test.base, test.ref)
		if got != test.expected {
			t.Errorf("For %q + %q got %q; expected %q", test.base, test.ref, got, test.expected)
		}
	}
}

func BenchmarkResolvePath(b *testing.B) {
	b.Run("Simple", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resolvePath("a/b/c", ".././d")
		}
	})
	b.Run("Deep", func(b *testing.B) {
		base := strings.Repeat("a/", 100) + "b"
		ref := "c"
		b.ResetTimer()
		for b.Loop() {
			resolvePath(base, ref)
		}
	})
	b.Run("Backtrack", func(b *testing.B) {
		base := strings.Repeat("a/", 100) + "b"
		ref := strings.Repeat("../", 50) + "c"
		b.ResetTimer()
		for b.Loop() {
			resolvePath(base, ref)
		}
	})
}

var resolveReferenceTests = []struct {
	base, rel, expected string
}{
	{"http://foo.com?a=b", "https://bar.com/", "https://bar.com/"},
	{"http://foo.com/", "https://bar.com/?a=b", "https://bar.com/?a=b"},
	{"http://foo.com/", "https://bar.com/?", "https://bar.com/?"},
	{"http://foo.com/bar", "mailto:foo@example.com", "mailto:foo@example.com"},

	{"http://foo.com/bar", "/baz", "http://foo.com/baz"},
	{"http://foo.com/bar?a=b#f", "/baz", "http://foo.com/baz"},
	{"http://foo.com/bar?a=b", "/baz?", "http://foo.com/baz?"},
	{"http://foo.com/bar?a=b", "/baz?c=d", "http://foo.com/baz?c=d"},

	{"http://foo.com/bar", "http://foo.com//baz", "http://foo.com//baz"},
	{"http://foo.com/bar", "http://foo.com///baz/quux", "http://foo.com///baz/quux"},

	{"https://foo.com/bar?a=b", "//bar.com/quux", "https://bar.com/quux"},

	{"http://foo.com", ".", "http://foo.com/"},
	{"http://foo.com/bar", ".", "http://foo.com/"},
	{"http://foo.com/bar/", ".", "http://foo.com/bar/"},

	{"http://foo.com", "bar", "http://foo.com/bar"},
	{"http://foo.com/", "bar", "http://foo.com/bar"},
	{"http://foo.com/bar/baz", "quux", "http://foo.com/bar/quux"},
	{"http://foo.com/bar/baz/", "quux", "http://foo.com/bar/baz/quux"},

	{"http://foo.com/bar/baz", "../quux", "http://foo.com/quux"},
	{"http://foo.com/bar/baz", "../../../../../quux", "http://foo.com/quux"},
	{"http://foo.com/bar", "..", "http://foo.com/"},
	{"http://foo.com/bar/baz", "./..", "http://foo.com/"},

	{"http://foo.com/bar/baz", "quux/dotdot/../tail", "http://foo.com/bar/quux/tail"},
	{"http://foo.com/bar/baz", "quux/./dotdot/../tail", "http://foo.com/bar/quux/tail"},
	{"http://foo.com/bar/baz", "quux/./dotdot/.././tail", "http://foo.com/bar/quux/tail"},
	{"http://foo.com/bar/baz", "quux/./dotdot/./../tail", "http://foo.com/bar/quux/tail"},
	{"http://foo.com/bar/baz", "quux/./dotdot/dotdot/././../../tail", "http://foo.com/bar/quux/tail"},
	{"http://foo.com/bar/baz", "quux/./dotdot/dotdot/./.././../tail", "http://foo.com/bar/quux/tail"},
	{"http://foo.com/bar/baz", "quux/./dotdot/dotdot/dotdot/./../../.././././tail", "http://foo.com/bar/quux/tail"},
	{"http://foo.com/bar/baz", "quux/./dotdot/../dotdot/../dot/./tail/..", "http://foo.com/bar/quux/dot/"},

	{"http://foo.com/dot/./dotdot/../foo/bar", "../baz", "http://foo.com/dot/baz"},

	{"http://foo.com/bar", "...", "http://foo.com/..."},

	{"http://foo.com/bar", ".#frag", "http://foo.com/#frag"},
	{"http://example.org/", "#!$&%27()*+,;=", "http://example.org/#!$&%27()*+,;="},

	{"http://foo.com/foo%2fbar/", "../baz", "http://foo.com/baz"},
	{"http://foo.com/1/2%2f/3%2f4/5", "../../a/b/c", "http://foo.com/1/a/b/c"},
	{"http://foo.com/1/2/3", "./a%2f../../b/..%2fc", "http://foo.com/1/2/b/..%2fc"},
	{"http://foo.com/1/2%2f/3%2f4/5", "./a%2f../b/../c", "http://foo.com/1/2%2f/3%2f4/a%2f../c"},
	{"http://foo.com/foo%20bar/", "../baz", "http://foo.com/baz"},
	{"http://foo.com/foo", "../bar%2fbaz", "http://foo.com/bar%2fbaz"},
	{"http://foo.com/foo%2dbar/", "./baz-quux", "http://foo.com/foo%2dbar/baz-quux"},

	{"http://a/b/c/d;p?q", "g:h", "g:h"},
	{"http://a/b/c/d;p?q", "g", "http://a/b/c/g"},
	{"http://a/b/c/d;p?q", "./g", "http://a/b/c/g"},
	{"http://a/b/c/d;p?q", "g/", "http://a/b/c/g/"},
	{"http://a/b/c/d;p?q", "/g", "http://a/g"},
	{"http://a/b/c/d;p?q", "//g", "http://g"},
	{"http://a/b/c/d;p?q", "?y", "http://a/b/c/d;p?y"},
	{"http://a/b/c/d;p?q", "g?y", "http://a/b/c/g?y"},
	{"http://a/b/c/d;p?q", "#s", "http://a/b/c/d;p?q#s"},
	{"http://a/b/c/d;p?q", "g#s", "http://a/b/c/g#s"},
	{"http://a/b/c/d;p?q", "g?y#s", "http://a/b/c/g?y#s"},
	{"http://a/b/c/d;p?q", ";x", "http://a/b/c/;x"},
	{"http://a/b/c/d;p?q", "g;x", "http://a/b/c/g;x"},
	{"http://a/b/c/d;p?q", "g;x?y#s", "http://a/b/c/g;x?y#s"},
	{"http://a/b/c/d;p?q", "", "http://a/b/c/d;p?q"},
	{"http://a/b/c/d;p?q", ".", "http://a/b/c/"},
	{"http://a/b/c/d;p?q", "./", "http://a/b/c/"},
	{"http://a/b/c/d;p?q", "..", "http://a/b/"},
	{"http://a/b/c/d;p?q", "../", "http://a/b/"},
	{"http://a/b/c/d;p?q", "../g", "http://a/b/g"},
	{"http://a/b/c/d;p?q", "../..", "http://a/"},
	{"http://a/b/c/d;p?q", "../../", "http://a/"},
	{"http://a/b/c/d;p?q", "../../g", "http://a/g"},

	{"http://a/b/c/d;p?q", "../../../g", "http://a/g"},
	{"http://a/b/c/d;p?q", "../../../../g", "http://a/g"},
	{"http://a/b/c/d;p?q", "/./g", "http://a/g"},
	{"http://a/b/c/d;p?q", "/../g", "http://a/g"},
	{"http://a/b/c/d;p?q", "g.", "http://a/b/c/g."},
	{"http://a/b/c/d;p?q", ".g", "http://a/b/c/.g"},
	{"http://a/b/c/d;p?q", "g..", "http://a/b/c/g.."},
	{"http://a/b/c/d;p?q", "..g", "http://a/b/c/..g"},
	{"http://a/b/c/d;p?q", "./../g", "http://a/b/g"},
	{"http://a/b/c/d;p?q", "./g/.", "http://a/b/c/g/"},
	{"http://a/b/c/d;p?q", "g/./h", "http://a/b/c/g/h"},
	{"http://a/b/c/d;p?q", "g/../h", "http://a/b/c/h"},
	{"http://a/b/c/d;p?q", "g;x=1/./y", "http://a/b/c/g;x=1/y"},
	{"http://a/b/c/d;p?q", "g;x=1/../y", "http://a/b/c/y"},
	{"http://a/b/c/d;p?q", "g?y/./x", "http://a/b/c/g?y/./x"},
	{"http://a/b/c/d;p?q", "g?y/../x", "http://a/b/c/g?y/../x"},
	{"http://a/b/c/d;p?q", "g#s/./x", "http://a/b/c/g#s/./x"},
	{"http://a/b/c/d;p?q", "g#s/../x", "http://a/b/c/g#s/../x"},

	{"https://a/b/c/d;p?q", "//g?q", "https://g?q"},
	{"https://a/b/c/d;p?q", "//g#s", "https://g#s"},
	{"https://a/b/c/d;p?q", "//g/d/e/f?y#s", "https://g/d/e/f?y#s"},
	{"https://a/b/c/d;p#s", "?y", "https://a/b/c/d;p?y"},
	{"https://a/b/c/d;p?q#s", "?y", "https://a/b/c/d;p?y"},

	{"https://a/b/c/d;p?q#s", "?", "https://a/b/c/d;p?"},

	{"https://foo.com/bar?a=b", "http:opaque", "http:opaque"},
	{"http:opaque?x=y#zzz", "https:/foo?a=b#frag", "https:/foo?a=b#frag"},
	{"http:opaque?x=y#zzz", "https:foo:bar", "https:foo:bar"},
	{"http:opaque?x=y#zzz", "https:bar/baz?a=b#frag", "https:bar/baz?a=b#frag"},
	{"http:opaque?x=y#zzz", "https://user@host:1234?a=b#frag", "https://user@host:1234?a=b#frag"},
	{"http:opaque?x=y#zzz", "?a=b#frag", "http:opaque?a=b#frag"},
}

func TestResolveReference(t *testing.T) {
	mustParse := func(url string) *URL {
		u, err := Parse(url)
		if err != nil {
			t.Fatalf("Parse(%q) got err %v", url, err)
		}
		return u
	}
	opaque := &URL{Scheme: "scheme", Opaque: "opaque"}
	for _, test := range resolveReferenceTests {
		base := mustParse(test.base)
		rel := mustParse(test.rel)
		url := base.ResolveReference(rel)
		if got := url.String(); got != test.expected {
			t.Errorf("URL(%q).ResolveReference(%q)\ngot  %q\nwant %q", test.base, test.rel, got, test.expected)
		}

		if base == url {
			t.Errorf("Expected URL.ResolveReference to return new URL instance.")
		}

		url, err := base.Parse(test.rel)
		if err != nil {
			t.Errorf("URL(%q).Parse(%q) failed: %v", test.base, test.rel, err)
		} else if got := url.String(); got != test.expected {
			t.Errorf("URL(%q).Parse(%q)\ngot  %q\nwant %q", test.base, test.rel, got, test.expected)
		} else if base == url {
			t.Errorf("Expected URL.Parse to return new URL instance.")
		}

		url = base.ResolveReference(opaque)
		if *url != *opaque {
			t.Errorf("ResolveReference failed to resolve opaque URL:\ngot  %#v\nwant %#v", url, opaque)
		}

		url, err = base.Parse("scheme:opaque")
		if err != nil {
			t.Errorf(`URL(%q).Parse("scheme:opaque") failed: %v`, test.base, err)
		} else if *url != *opaque {
			t.Errorf("Parse failed to resolve opaque URL:\ngot  %#v\nwant %#v", opaque, url)
		} else if base == url {
			t.Errorf("Expected URL.Parse to return new URL instance.")
		}
	}
}

func TestQueryValues(t *testing.T) {
	u, _ := Parse("http://x.com?foo=bar&bar=1&bar=2&baz")
	v := u.Query()
	if len(v) != 3 {
		t.Errorf("got %d keys in Query values, want 3", len(v))
	}
	if g, e := v.Get("foo"), "bar"; g != e {
		t.Errorf("Get(foo) = %q, want %q", g, e)
	}

	if g, e := v.Get("Foo"), ""; g != e {
		t.Errorf("Get(Foo) = %q, want %q", g, e)
	}
	if g, e := v.Get("bar"), "1"; g != e {
		t.Errorf("Get(bar) = %q, want %q", g, e)
	}
	if g, e := v.Get("baz"), ""; g != e {
		t.Errorf("Get(baz) = %q, want %q", g, e)
	}
	if h, e := v.Has("foo"), true; h != e {
		t.Errorf("Has(foo) = %t, want %t", h, e)
	}
	if h, e := v.Has("bar"), true; h != e {
		t.Errorf("Has(bar) = %t, want %t", h, e)
	}
	if h, e := v.Has("baz"), true; h != e {
		t.Errorf("Has(baz) = %t, want %t", h, e)
	}
	if h, e := v.Has("noexist"), false; h != e {
		t.Errorf("Has(noexist) = %t, want %t", h, e)
	}
	v.Del("bar")
	if g, e := v.Get("bar"), ""; g != e {
		t.Errorf("second Get(bar) = %q, want %q", g, e)
	}
}

type parseTest struct {
	query string
	out   Values
	ok    bool
}

var parseTests = []parseTest{
	{
		query: "a=1",
		out:   Values{"a": []string{"1"}},
		ok:    true,
	},
	{
		query: "a=1&b=2",
		out:   Values{"a": []string{"1"}, "b": []string{"2"}},
		ok:    true,
	},
	{
		query: "a=1&a=2&a=banana",
		out:   Values{"a": []string{"1", "2", "banana"}},
		ok:    true,
	},
	{
		query: "ascii=%3Ckey%3A+0x90%3E",
		out:   Values{"ascii": []string{"<key: 0x90>"}},
		ok:    true,
	}, {
		query: "a=1;b=2",
		out:   Values{},
		ok:    false,
	}, {
		query: "a;b=1",
		out:   Values{},
		ok:    false,
	}, {
		query: "a=%3B",
		out:   Values{"a": []string{";"}},
		ok:    true,
	},
	{
		query: "a%3Bb=1",
		out:   Values{"a;b": []string{"1"}},
		ok:    true,
	},
	{
		query: "a=1&a=2;a=banana",
		out:   Values{"a": []string{"1"}},
		ok:    false,
	},
	{
		query: "a;b&c=1",
		out:   Values{"c": []string{"1"}},
		ok:    false,
	},
	{
		query: "a=1&b=2;a=3&c=4",
		out:   Values{"a": []string{"1"}, "c": []string{"4"}},
		ok:    false,
	},
	{
		query: "a=1&b=2;c=3",
		out:   Values{"a": []string{"1"}},
		ok:    false,
	},
	{
		query: ";",
		out:   Values{},
		ok:    false,
	},
	{
		query: "a=1;",
		out:   Values{},
		ok:    false,
	},
	{
		query: "a=1&;",
		out:   Values{"a": []string{"1"}},
		ok:    false,
	},
	{
		query: ";a=1&b=2",
		out:   Values{"b": []string{"2"}},
		ok:    false,
	},
	{
		query: "a=1&b=2;",
		out:   Values{"a": []string{"1"}},
		ok:    false,
	},
}

func TestParseQuery(t *testing.T) {
	for _, test := range parseTests {
		t.Run(test.query, func(t *testing.T) {
			form, err := ParseQuery(test.query)
			if test.ok != (err == nil) {
				want := "<error>"
				if test.ok {
					want = "<nil>"
				}
				t.Errorf("Unexpected error: %v, want %v", err, want)
			}
			if len(form) != len(test.out) {
				t.Errorf("len(form) = %d, want %d", len(form), len(test.out))
			}
			for k, evs := range test.out {
				vs, ok := form[k]
				if !ok {
					t.Errorf("Missing key %q", k)
					continue
				}
				if len(vs) != len(evs) {
					t.Errorf("len(form[%q]) = %d, want %d", k, len(vs), len(evs))
					continue
				}
				for j, ev := range evs {
					if v := vs[j]; v != ev {
						t.Errorf("form[%q][%d] = %q, want %q", k, j, v, ev)
					}
				}
			}
		})
	}
}

func TestParseQueryLimits(t *testing.T) {
	for _, test := range []struct {
		params  int
		godebug string
		wantErr bool
	}{{
		params:  10,
		wantErr: false,
	}, {
		params:  defaultMaxParams,
		wantErr: false,
	}, {
		params:  defaultMaxParams + 1,
		wantErr: true,
	}} {
		want := Values{}
		var b strings.Builder
		for i := range test.params {
			if i > 0 {
				b.WriteString("&")
			}
			p := fmt.Sprintf("p%v", i)
			b.WriteString(p)
			want[p] = []string{""}
		}
		query := b.String()
		got, err := ParseQuery(query)
		if gotErr, wantErr := err != nil, test.wantErr; gotErr != wantErr {
			t.Errorf("GODEBUG=%v ParseQuery(%v params) = %v, want error: %v", test.godebug, test.params, err, wantErr)
		}
		if err != nil {
			continue
		}
		if got, want := len(got), test.params; got != want {
			t.Errorf("GODEBUG=%v ParseQuery(%v params): got %v params, want %v", test.godebug, test.params, got, want)
		}
	}
}

type RequestURITest struct {
	url *URL
	out string
}

var requritests = []RequestURITest{
	{
		&URL{
			Scheme: "http",
			Host:   "example.com",
			Path:   "",
		},
		"/",
	},
	{
		&URL{
			Scheme: "http",
			Host:   "example.com",
			Path:   "/a b",
		},
		"/a%20b",
	},

	{
		&URL{
			Scheme: "http",
			Host:   "example.com",
			Opaque: "/%2F/%2F/",
		},
		"/%2F/%2F/",
	},

	{
		&URL{
			Scheme: "http",
			Host:   "example.com",
			Opaque: "//other.example.com/%2F/%2F/",
		},
		"http://other.example.com/%2F/%2F/",
	},

	{
		&URL{
			Scheme:  "http",
			Host:    "example.com",
			Path:    "/////",
			RawPath: "/%2F/%2F/",
		},
		"/%2F/%2F/",
	},
	{
		&URL{
			Scheme:  "http",
			Host:    "example.com",
			Path:    "/////",
			RawPath: "/WRONG/",
		},
		"/////",
	},
	{
		&URL{
			Scheme:   "http",
			Host:     "example.com",
			Path:     "/a b",
			RawQuery: "q=go+language",
		},
		"/a%20b?q=go+language",
	},
	{
		&URL{
			Scheme:   "http",
			Host:     "example.com",
			Path:     "/a b",
			RawPath:  "/a b",
			RawQuery: "q=go+language",
		},
		"/a%20b?q=go+language",
	},
	{
		&URL{
			Scheme:   "http",
			Host:     "example.com",
			Path:     "/a?b",
			RawPath:  "/a?b",
			RawQuery: "q=go+language",
		},
		"/a%3Fb?q=go+language",
	},
	{
		&URL{
			Scheme: "myschema",
			Opaque: "opaque",
		},
		"opaque",
	},
	{
		&URL{
			Scheme:   "myschema",
			Opaque:   "opaque",
			RawQuery: "q=go+language",
		},
		"opaque?q=go+language",
	},
	{
		&URL{
			Scheme: "http",
			Host:   "example.com",
			Path:   "//foo",
		},
		"//foo",
	},
	{
		&URL{
			Scheme:     "http",
			Host:       "example.com",
			Path:       "/foo",
			ForceQuery: true,
		},
		"/foo?",
	},
}

func TestRequestURI(t *testing.T) {
	for _, tt := range requritests {
		s := tt.url.RequestURI()
		if s != tt.out {
			t.Errorf("%#v.RequestURI() == %q (expected %q)", tt.url, s, tt.out)
		}
	}
}

func TestParseFailure(t *testing.T) {
	const url = "%gh&%ij"
	_, err := ParseQuery(url)
	errStr := fmt.Sprint(err)
	if !strings.Contains(errStr, "%gh") {
		t.Errorf(`ParseQuery(%q) returned error %q, want something containing %q"`, url, errStr, "%gh")
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"http://[::1]", false},
		{"http://[::1]:80", false},
		{"http://[::1]:namedport", true},
		{"http://x:namedport", true},
		{"http://[::1]/", false},
		{"http://[::1]a", true},
		{"http://[::1]%23", true},
		{"http://[::1%25en0]", false},
		{"http://[::1]:", false},
		{"http://x:", false},
		{"http://[::1]:%38%30", true},
		{"http://[::1%25%41]", false},
		{"http://[%10::1]", true},
		{"http://[::1]/%48", false},
		{"http://%41:8080/", true},
		{"mysql://x@y(z:123)/foo", true},
		{"mysql://x@y(1.2.3.4:123)/foo", true},

		{" http://foo.com", true},
		{"ht tp://foo.com", true},
		{"ahttp://foo.com", false},
		{"1http://foo.com", true},

		{"http://[]%20%48%54%54%50%2f%31%2e%31%0a%4d%79%48%65%61%64%65%72%3a%20%31%32%33%0a%0a/", true},
		{"http://a b.com/", true},
		{"cache_object://foo", true},
		{"cache_object:foo", true},
		{"cache_object:foo/bar", true},
		{"cache_object/:foo/bar", false},

		{"http://[192.168.0.1]/", true},
		{"http://[192.168.0.1]:8080/", true},
		{"http://[::ffff:192.168.0.1]/", false},
		{"http://[::ffff:192.168.0.1000]/", true},
		{"http://[::ffff:192.168.0.1]:8080/", false},
		{"http://[::ffff:c0a8:1]/", false},
		{"http://[not-an-ip]/", true},
		{"http://[fe80::1%foo]/", true},
		{"http://[fe80::1", true},
		{"http://fe80::1]/", true},
		{"http://[test.com]/", true},
		{"http://example.com[::1]", true},
		{"http://example.com[::1", true},
		{"http://[::1", true},
		{"http://.[::1]", true},
		{"http:// [::1]", true},
		{"hxxp://mathepqo[.]serveftp(.)com:9059", true},
	}
	for _, tt := range tests {
		u, err := Parse(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("Parse(%q) = %#v; want an error", tt.in, u)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q) = %v; want no error", tt.in, err)
		}
	}
}

func TestStarRequest(t *testing.T) {
	u, err := Parse("*")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := u.RequestURI(), "*"; got != want {
		t.Errorf("RequestURI = %q; want %q", got, want)
	}
}

type shouldEscapeTest struct {
	in     byte
	mode   encoding
	escape bool
}

var shouldEscapeTests = []shouldEscapeTest{
	{'a', encodePath, false},
	{'a', encodeUserPassword, false},
	{'a', encodeQueryComponent, false},
	{'a', encodeFragment, false},
	{'a', encodeHost, false},
	{'z', encodePath, false},
	{'A', encodePath, false},
	{'Z', encodePath, false},
	{'0', encodePath, false},
	{'9', encodePath, false},
	{'-', encodePath, false},
	{'-', encodeUserPassword, false},
	{'-', encodeQueryComponent, false},
	{'-', encodeFragment, false},
	{'.', encodePath, false},
	{'_', encodePath, false},
	{'~', encodePath, false},

	{':', encodeUserPassword, true},
	{'/', encodeUserPassword, true},
	{'?', encodeUserPassword, true},
	{'@', encodeUserPassword, true},
	{'$', encodeUserPassword, false},
	{'&', encodeUserPassword, false},
	{'+', encodeUserPassword, false},
	{',', encodeUserPassword, false},
	{';', encodeUserPassword, false},
	{'=', encodeUserPassword, false},

	{'!', encodeHost, false},
	{'$', encodeHost, false},
	{'&', encodeHost, false},
	{'\'', encodeHost, false},
	{'(', encodeHost, false},
	{')', encodeHost, false},
	{'*', encodeHost, false},
	{'+', encodeHost, false},
	{',', encodeHost, false},
	{';', encodeHost, false},
	{'=', encodeHost, false},
	{':', encodeHost, false},
	{'[', encodeHost, false},
	{']', encodeHost, false},
	{'0', encodeHost, false},
	{'9', encodeHost, false},
	{'A', encodeHost, false},
	{'z', encodeHost, false},
	{'_', encodeHost, false},
	{'-', encodeHost, false},
	{'.', encodeHost, false},
}

func TestShouldEscape(t *testing.T) {
	for _, tt := range shouldEscapeTests {
		if shouldEscape(tt.in, tt.mode) != tt.escape {
			t.Errorf("shouldEscape(%q, %v) returned %v; expected %v", tt.in, tt.mode, !tt.escape, tt.escape)
		}
	}
}

type timeoutError struct {
	timeout bool
}

func (e *timeoutError) Error() string { return "timeout error" }
func (e *timeoutError) Timeout() bool { return e.timeout }

type temporaryError struct {
	temporary bool
}

func (e *temporaryError) Error() string   { return "temporary error" }
func (e *temporaryError) Temporary() bool { return e.temporary }

type timeoutTemporaryError struct {
	timeoutError
	temporaryError
}

func (e *timeoutTemporaryError) Error() string { return "timeout/temporary error" }

var netErrorTests = []struct {
	err       error
	timeout   bool
	temporary bool
}{{
	err:       &Error{"Get", "http://google.com/", &timeoutError{timeout: true}},
	timeout:   true,
	temporary: false,
}, {
	err:       &Error{"Get", "http://google.com/", &timeoutError{timeout: false}},
	timeout:   false,
	temporary: false,
}, {
	err:       &Error{"Get", "http://google.com/", &temporaryError{temporary: true}},
	timeout:   false,
	temporary: true,
}, {
	err:       &Error{"Get", "http://google.com/", &temporaryError{temporary: false}},
	timeout:   false,
	temporary: false,
}, {
	err:       &Error{"Get", "http://google.com/", &timeoutTemporaryError{timeoutError{timeout: true}, temporaryError{temporary: true}}},
	timeout:   true,
	temporary: true,
}, {
	err:       &Error{"Get", "http://google.com/", &timeoutTemporaryError{timeoutError{timeout: false}, temporaryError{temporary: true}}},
	timeout:   false,
	temporary: true,
}, {
	err:       &Error{"Get", "http://google.com/", &timeoutTemporaryError{timeoutError{timeout: true}, temporaryError{temporary: false}}},
	timeout:   true,
	temporary: false,
}, {
	err:       &Error{"Get", "http://google.com/", &timeoutTemporaryError{timeoutError{timeout: false}, temporaryError{temporary: false}}},
	timeout:   false,
	temporary: false,
}, {
	err:       &Error{"Get", "http://google.com/", io.EOF},
	timeout:   false,
	temporary: false,
}}

func TestURLErrorImplementsNetError(t *testing.T) {
	for i, tt := range netErrorTests {
		err, ok := tt.err.(net.Error)
		if !ok {
			t.Errorf("%d: %T does not implement net.Error", i+1, tt.err)
			continue
		}
		if err.Timeout() != tt.timeout {
			t.Errorf("%d: err.Timeout(): got %v, want %v", i+1, err.Timeout(), tt.timeout)
			continue
		}
		if err.Temporary() != tt.temporary {
			t.Errorf("%d: err.Temporary(): got %v, want %v", i+1, err.Temporary(), tt.temporary)
		}
	}
}

func TestURLHostnameAndPort(t *testing.T) {
	tests := []struct {
		in   string
		host string
		port string
	}{
		{"foo.com:80", "foo.com", "80"},
		{"foo.com", "foo.com", ""},
		{"foo.com:", "foo.com", ""},
		{"FOO.COM", "FOO.COM", ""},
		{"1.2.3.4", "1.2.3.4", ""},
		{"1.2.3.4:80", "1.2.3.4", "80"},
		{"[1:2:3:4]", "1:2:3:4", ""},
		{"[1:2:3:4]:80", "1:2:3:4", "80"},
		{"[::1]:80", "::1", "80"},
		{"[::1]", "::1", ""},
		{"[::1]:", "::1", ""},
		{"localhost", "localhost", ""},
		{"localhost:443", "localhost", "443"},
		{"some.super.long.domain.example.org:8080", "some.super.long.domain.example.org", "8080"},
		{"[2001:0db8:85a3:0000:0000:8a2e:0370:7334]:17000", "2001:0db8:85a3:0000:0000:8a2e:0370:7334", "17000"},
		{"[2001:0db8:85a3:0000:0000:8a2e:0370:7334]", "2001:0db8:85a3:0000:0000:8a2e:0370:7334", ""},

		{"[google.com]:80", "google.com", "80"},
		{"google.com]:80", "google.com]", "80"},
		{"google.com:80_invalid_port", "google.com:80_invalid_port", ""},
		{"[::1]extra]:80", "::1]extra", "80"},
		{"google.com]extra:extra", "google.com]extra:extra", ""},
	}
	for _, tt := range tests {
		u := &URL{Host: tt.in}
		host, port := u.Hostname(), u.Port()
		if host != tt.host {
			t.Errorf("Hostname for Host %q = %q; want %q", tt.in, host, tt.host)
		}
		if port != tt.port {
			t.Errorf("Port for Host %q = %q; want %q", tt.in, port, tt.port)
		}
	}
}

var _ encodingPkg.BinaryMarshaler = (*URL)(nil)
var _ encodingPkg.BinaryUnmarshaler = (*URL)(nil)
var _ encodingPkg.BinaryAppender = (*URL)(nil)

func TestJSON(t *testing.T) {
	u, err := Parse("https://www.google.com/x?y=z")
	if err != nil {
		t.Fatal(err)
	}
	js, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}

	u1 := new(URL)
	err = json.Unmarshal(js, u1)
	if err != nil {
		t.Fatal(err)
	}
	if u1.String() != u.String() {
		t.Errorf("json decoded to: %s\nwant: %s\n", u1, u)
	}
}

func TestGob(t *testing.T) {
	u, err := Parse("https://www.google.com/x?y=z")
	if err != nil {
		t.Fatal(err)
	}
	var w bytes.Buffer
	err = gob.NewEncoder(&w).Encode(u)
	if err != nil {
		t.Fatal(err)
	}

	u1 := new(URL)
	err = gob.NewDecoder(&w).Decode(u1)
	if err != nil {
		t.Fatal(err)
	}
	if u1.String() != u.String() {
		t.Errorf("json decoded to: %s\nwant: %s\n", u1, u)
	}
}

func TestNilUser(t *testing.T) {
	defer func() {
		if v := recover(); v != nil {
			t.Fatalf("unexpected panic: %v", v)
		}
	}()

	u, err := Parse("http://foo.com/")

	if err != nil {
		t.Fatalf("parse err: %v", err)
	}

	if v := u.User.Username(); v != "" {
		t.Fatalf("expected empty username, got %s", v)
	}

	if v, ok := u.User.Password(); v != "" || ok {
		t.Fatalf("expected empty password, got %s (%v)", v, ok)
	}

	if v := u.User.String(); v != "" {
		t.Fatalf("expected empty string, got %s", v)
	}
}

func TestInvalidUserPassword(t *testing.T) {
	_, err := Parse("http://user^:passwo^rd@foo.com/")
	if got, wantsub := fmt.Sprint(err), "net/url: invalid userinfo"; !strings.Contains(got, wantsub) {
		t.Errorf("error = %q; want substring %q", got, wantsub)
	}
}

func TestRejectControlCharacters(t *testing.T) {
	tests := []string{
		"http://foo.com/?foo\nbar",
		"http\r://foo.com/",
		"http://foo\x7f.com/",
	}
	for _, s := range tests {
		_, err := Parse(s)
		const wantSub = "net/url: invalid control character in URL"
		if got := fmt.Sprint(err); !strings.Contains(got, wantSub) {
			t.Errorf("Parse(%q) error = %q; want substring %q", s, got, wantSub)
		}
	}

	if _, err := Parse("http://foo.com/ctl\x80"); err != nil {
		t.Errorf("error parsing URL with non-ASCII control byte: %v", err)
	}
}

var escapeBenchmarks = []struct {
	unescaped string
	query     string
	path      string
}{
	{
		unescaped: "one two",
		query:     "one+two",
		path:      "one%20two",
	},
	{
		unescaped: "Фотки собак",
		query:     "%D0%A4%D0%BE%D1%82%D0%BA%D0%B8+%D1%81%D0%BE%D0%B1%D0%B0%D0%BA",
		path:      "%D0%A4%D0%BE%D1%82%D0%BA%D0%B8%20%D1%81%D0%BE%D0%B1%D0%B0%D0%BA",
	},

	{
		unescaped: "shortrun(break)shortrun",
		query:     "shortrun%28break%29shortrun",
		path:      "shortrun%28break%29shortrun",
	},

	{
		unescaped: "longerrunofcharacters(break)anotherlongerrunofcharacters",
		query:     "longerrunofcharacters%28break%29anotherlongerrunofcharacters",
		path:      "longerrunofcharacters%28break%29anotherlongerrunofcharacters",
	},

	{
		unescaped: strings.Repeat("padded/with+various%characters?that=need$some@escaping+paddedsowebreak/256bytes", 4),
		query:     strings.Repeat("padded%2Fwith%2Bvarious%25characters%3Fthat%3Dneed%24some%40escaping%2Bpaddedsowebreak%2F256bytes", 4),
		path:      strings.Repeat("padded%2Fwith+various%25characters%3Fthat=need$some@escaping+paddedsowebreak%2F256bytes", 4),
	},
}

func BenchmarkQueryEscape(b *testing.B) {
	for _, tc := range escapeBenchmarks {
		b.Run("", func(b *testing.B) {
			b.ReportAllocs()
			var g string
			for i := 0; i < b.N; i++ {
				g = QueryEscape(tc.unescaped)
			}
			b.StopTimer()
			if g != tc.query {
				b.Errorf("QueryEscape(%q) == %q, want %q", tc.unescaped, g, tc.query)
			}
		})
	}
}

func BenchmarkPathEscape(b *testing.B) {
	for _, tc := range escapeBenchmarks {
		b.Run("", func(b *testing.B) {
			b.ReportAllocs()
			var g string
			for i := 0; i < b.N; i++ {
				g = PathEscape(tc.unescaped)
			}
			b.StopTimer()
			if g != tc.path {
				b.Errorf("PathEscape(%q) == %q, want %q", tc.unescaped, g, tc.path)
			}
		})
	}
}

func BenchmarkQueryUnescape(b *testing.B) {
	for _, tc := range escapeBenchmarks {
		b.Run("", func(b *testing.B) {
			b.ReportAllocs()
			var g string
			for i := 0; i < b.N; i++ {
				g, _ = QueryUnescape(tc.query)
			}
			b.StopTimer()
			if g != tc.unescaped {
				b.Errorf("QueryUnescape(%q) == %q, want %q", tc.query, g, tc.unescaped)
			}
		})
	}
}

func BenchmarkPathUnescape(b *testing.B) {
	for _, tc := range escapeBenchmarks {
		b.Run("", func(b *testing.B) {
			b.ReportAllocs()
			var g string
			for i := 0; i < b.N; i++ {
				g, _ = PathUnescape(tc.path)
			}
			b.StopTimer()
			if g != tc.unescaped {
				b.Errorf("PathUnescape(%q) == %q, want %q", tc.path, g, tc.unescaped)
			}
		})
	}
}

func TestJoinPath(t *testing.T) {
	tests := []struct {
		base string
		elem []string
		out  string
	}{
		{
			base: "https://go.googlesource.com",
			elem: []string{"go"},
			out:  "https://go.googlesource.com/go",
		},
		{
			base: "https://go.googlesource.com/a/b/c",
			elem: []string{"../../../go"},
			out:  "https://go.googlesource.com/go",
		},
		{
			base: "https://go.googlesource.com/",
			elem: []string{"../go"},
			out:  "https://go.googlesource.com/go",
		},
		{
			base: "https://go.googlesource.com",
			elem: []string{"../go", "../../go", "../../../go"},
			out:  "https://go.googlesource.com/go",
		},
		{
			base: "https://go.googlesource.com/../go",
			elem: nil,
			out:  "https://go.googlesource.com/go",
		},
		{
			base: "https://go.googlesource.com/",
			elem: []string{"./go"},
			out:  "https://go.googlesource.com/go",
		},
		{
			base: "https://go.googlesource.com//",
			elem: []string{"/go"},
			out:  "https://go.googlesource.com/go",
		},
		{
			base: "https://go.googlesource.com//",
			elem: []string{"/go", "a", "b", "c"},
			out:  "https://go.googlesource.com/go/a/b/c",
		},
		{
			base: "http://[fe80::1%en0]:8080/",
			elem: []string{"/go"},
		},
		{
			base: "https://go.googlesource.com",
			elem: []string{"go/"},
			out:  "https://go.googlesource.com/go/",
		},
		{
			base: "https://go.googlesource.com",
			elem: []string{"go//"},
			out:  "https://go.googlesource.com/go/",
		},
		{
			base: "https://go.googlesource.com",
			elem: nil,
			out:  "https://go.googlesource.com/",
		},
		{
			base: "https://go.googlesource.com/",
			elem: nil,
			out:  "https://go.googlesource.com/",
		},
		{
			base: "https://go.googlesource.com/a%2fb",
			elem: []string{"c"},
			out:  "https://go.googlesource.com/a%2fb/c",
		},
		{
			base: "https://go.googlesource.com/a%2fb",
			elem: []string{"c%2fd"},
			out:  "https://go.googlesource.com/a%2fb/c%2fd",
		},
		{
			base: "https://go.googlesource.com/a/b",
			elem: []string{"/go"},
			out:  "https://go.googlesource.com/a/b/go",
		},
		{
			base: "https://go.googlesource.com/",
			elem: []string{"100%"},
		},
		{
			base: "/",
			elem: nil,
			out:  "/",
		},
		{
			base: "a",
			elem: nil,
			out:  "a",
		},
		{
			base: "a",
			elem: []string{"b"},
			out:  "a/b",
		},
		{
			base: "a",
			elem: []string{"../b"},
			out:  "b",
		},
		{
			base: "a",
			elem: []string{"../../b"},
			out:  "b",
		},
		{
			base: "",
			elem: []string{"a"},
			out:  "a",
		},
		{
			base: "",
			elem: []string{"../a"},
			out:  "a",
		},
	}
	for _, tt := range tests {
		wantErr := "nil"
		if tt.out == "" {
			wantErr = "non-nil error"
		}
		out, err := JoinPath(tt.base, tt.elem...)
		if out != tt.out || (err == nil) != (tt.out != "") {
			t.Errorf("JoinPath(%q, %q) = %q, %v, want %q, %v", tt.base, tt.elem, out, err, tt.out, wantErr)
		}

		u, err := Parse(tt.base)
		if err != nil {
			if tt.out != "" {
				t.Errorf("Parse(%q) = %v", tt.base, err)
			}
			continue
		}
		if tt.out == "" {
			tt.out = tt.base
		}
		out = u.JoinPath(tt.elem...).String()
		if out != tt.out {
			t.Errorf("Parse(%q).JoinPath(%q) = %q, want %q", tt.base, tt.elem, out, tt.out)
		}
	}
}

func TestURLClone(t *testing.T) {
	tests := []struct {
		name string
		in   *URL
	}{
		{"nil", nil},
		{"zero value", &URL{}},
		{
			"Populated but nil .User",
			&URL{
				User:     nil,
				Host:     "foo",
				Path:     "/path",
				RawQuery: "a=b",
			},
		},
		{
			"non-nil .User",
			&URL{
				User:     User("user"),
				Host:     "foo",
				Path:     "/path",
				RawQuery: "a=b",
			},
		},
		{
			"non-nil .User: user and password set",
			&URL{
				User:     UserPassword("user", "password"),
				Host:     "foo",
				Path:     "/path",
				RawQuery: "a=b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloned := tt.in.Clone()
			if !reflect.DeepEqual(tt.in, cloned) {
				t.Fatalf("Differing values\n%s",
					"original: "+tt.in.String()+"\ncloned: "+cloned.String())
			}
			if tt.in == nil {
				return
			}

			if tt.in == cloned {
				t.Fatalf("URL: same pointer returned: %p", cloned)
			}

			cloned.Scheme = "https"
			if cloned.Scheme == tt.in.Scheme {
				t.Error("Inconsistent state: cloned.scheme changed and reflected in the input's scheme")
			}
			if reflect.DeepEqual(tt.in, cloned) {
				t.Fatal("Inconsistent state: cloned and input are somehow the same")
			}

			if !reflect.DeepEqual(tt.in.User, cloned.User) {
				t.Fatalf("Differing .User\n%s",
					"original: "+tt.in.String()+"\ncloned: "+cloned.String())
			}
			bothNil := tt.in.User == nil && cloned.User == nil
			if !bothNil && tt.in.User == cloned.User {
				t.Fatalf(".User: same pointer returned: %p", cloned.User)
			}
		})
	}
}

func TestValuesClone(t *testing.T) {
	tests := []struct {
		name string
		in   Values
	}{
		{"nil", nil},
		{"empty", Values{}},
		{"1 key, nil values", Values{"1": nil}},
		{"1 key, no values", Values{"1": {}}},
		{"1 key, some values", Values{"1": {"a", "b"}}},
		{"multiple keys, diverse values", Values{"1": {"a", "b"}, "X": nil, "B": {"abcdefghi"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cloned1 := tt.in.Clone()
			if !reflect.DeepEqual(tt.in, cloned1) {
				t.Fatal("reflect.DeepEqual failed")
			}

			if cloned1 == nil && tt.in == nil {
				return
			}
			if len(cloned1) == 0 && len(tt.in) == 0 && (cloned1 == nil || tt.in == nil) {
				t.Fatalf("Inconsistency: both have len=0, yet not both nil\nCloned: %#v\nOriginal: %#v\n", cloned1, tt.in)
			}

			cloned1["XXXXXXXXXXX"] = []string{"a", "b"}
			if reflect.DeepEqual(tt.in, cloned1) {
				t.Fatal("Inconsistent state: cloned and input are somehow the same")
			}

			cloned2 := tt.in.Clone()
			if !reflect.DeepEqual(tt.in, cloned2) {
				t.Fatal("reflect.DeepEqual failed")
			}
			cloned2.Add("a", "A")
			if !cloned2.Has("a") {
				t.Error("Cloned doesn't have the desired key: a")
			}
			if !cloned2.Has("a") {
				t.Error("Cloned doesn't have the desired key: a")
			}

			if reflect.DeepEqual(tt.in, cloned2) {
				t.Fatal("reflect.DeepEqual unexpectedly passed after modify cloned")
			}
			cloned2.Del("a")

			if !reflect.DeepEqual(tt.in, cloned2) {
				t.Fatal("reflect.DeepEqual failed")
			}

			cloned3 := tt.in.Clone()
			clonedKeys := slices.Collect(maps.Keys(cloned3))
			if len(clonedKeys) == 0 {
				return
			}
			key0 := clonedKeys[0]

			if len(cloned3[key0]) == 0 {
				cloned3[key0] = append(cloned3[key0], "golang")
			} else {
				cloned3[key0][0] = "directly modified"
				if got, want := cloned3.Get(key0), "directly modified"; got != want {
					t.Errorf("Get failed:\n\tGot:  %q\n\tWant: %q", got, want)
				}
			}
			if reflect.DeepEqual(tt.in, cloned3) {
				t.Fatal("reflect.DeepEqual unexpectedly passed after modify cloned")
			}

			cloned4 := tt.in.Clone()
			if !reflect.DeepEqual(tt.in, cloned4) {
				t.Fatal("reflect.DeepEqual failed")
			}
			cloned4.Set(key0, "good night")
			if reflect.DeepEqual(tt.in, cloned4) {
				t.Fatal("reflect.DeepEqual unexpectedly passed after modify cloned")
			}
			if got, want := cloned4.Get(key0), "good night"; got != want {
				t.Errorf("Get failed:\n\tGot:  %q\n\tWant: %q", got, want)
			}
		})
	}
}

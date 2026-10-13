package main

import (
	"github.com/eugenioenko/goalchemy/std/errors"
	"github.com/eugenioenko/goalchemy/std/net"
	"github.com/eugenioenko/goalchemy/std/net/url"
	"github.com/eugenioenko/goalchemy/std/strconv"
	"github.com/eugenioenko/goalchemy/std/strings"
)

func q(s string) string { return strconv.Quote(s) }

func b(v bool) string { return strconv.FormatBool(v) }

func describe(u *url.URL) string {
	user, pass, hasPass := "", "", false
	if u.User != nil {
		user = u.User.Username()
		pass, hasPass = u.User.Password()
	}
	return strings.Join([]string{
		q(u.Scheme), q(u.Opaque), q(user), q(pass), b(hasPass), q(u.Host), q(u.Path), q(u.RawPath),
		q(u.RawQuery), q(u.Fragment), q(u.RawFragment), b(u.ForceQuery), b(u.OmitHost),
		"|", q(u.String()), q(u.Hostname()), q(u.Port()), q(u.EscapedPath()), q(u.EscapedFragment()),
		b(u.IsAbs()), q(u.RequestURI()), q(u.Redacted()), q(u.Query().Encode()),
	}, " ")
}

func show(name string, u *url.URL, err error) {
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			println(name, "err", ue.Op, q(ue.URL), q(ue.Err.Error()), q(err.Error()))
			return
		}
		println(name, "err", q(err.Error()))
		return
	}
	println(name, describe(u))
}

var inputs = []string{
	"", "*", "http:", "http://", "//", "///x", "/", "a", "a:b", "./a:b", "a/b:c", "?", "#", "?a=1#b",
	"http://www.google.com", "http://www.google.com/", "http://www.google.com/file%20one%26two",
	"http://www.google.com/#file%20one%26two", "ftp://webmaster@www.google.com/", "ftp://john%20doe@www.google.com/",
	"http://www.google.com/?", "http://www.google.com/?foo=bar?", "http://www.google.com/?q=go+language",
	"http://www.google.com/?q=go%20language", "http://www.google.com/a%20b?q=c+d", "http:www.google.com/?q=go+language",
	"http:%2f%2fwww.google.com/?q=go+language", "mailto:/webmaster@golang.org", "mailto:webmaster@golang.org",
	"/foo?query=http://bad", "//foo", "//user@foo/path?a=b", "///threeslashes",
	"http://user:password@google.com", "http://j@ne:password@google.com", "http://jane:p@ssword@google.com",
	"http://j@ne:p@ssword@google.com/p@th?q=@go", "http://www.google.com/?q=go+language#foo",
	"http://www.google.com/?q=go+language#foo&bar", "http://www.google.com/?q=go+language#foo%26bar",
	"file:///home/adg/rabbits", "file:///C:/FooBar/Baz.txt", "MaIlTo:webmaster@golang.org", "a/b/c",
	"http://%3Fam:pa%3Fsword@google.com", "http://192.168.0.1/", "http://192.168.0.1:8080/", "http://[fe80::1]/",
	"http://[fe80::1]:8080/", "http://[fe80::1%25en0]/", "http://[fe80::1%25en0]:8080/",
	"http://[fe80::1%25%65%6e%301-._~]/", "http://[fe80::1%25%65%6e%301-._~]:8080/", "http://rest.rsc.io:8080/",
	"http://[::1]x", "http://[::1]:", "http://[127.0.0.1]/", "http://[::ffff:127.0.0.1]/", "http://[1::2::3]/",
	"http://[12345::]/", "http://[1.2.3.04]/", "http://[1:2:3:4:5:6:7::]/", "http://[1:2:3:4:5:6:7:8::]/",
	"http://[fe80::1%en0]/", "http://[fe80::1%25]/", "http://a[::1]/", "http://[::1", "http://::1]/",
	"http://host:port", "http://host:80:", "postgres://h1:1,h2:2/db", "mongodb://h1:1,h2:2/db",
	"http://us%zzer@host/", "http://us er@host/", "http://%41:%42@host/", "http://h%20st/", "http://h%41st/",
	"http://h%c3%a9st/", "http://héllo/", "http://host/%2f/%2F/a%20b?q=a+b#f%2f", "http://host/a?b?c",
	"http://host/a??", "http://host/a#", "http://host/a#b#c", "mailto:?subject=x", "urn:ietf:rfc:3986",
	"http://host\x00/", "http://host/\x7f", "1http://host", ":x", "http://host/a%", "http://host/a%2",
	"http://host/a%g0", "http://host/€", "http://host/ space", "https://KAS.Example.com:8443/kas?x=y#z",
	"HTTPS://HOST", "https:/example.com", "https:example.com", "https://example.com:65536",
	"https://1:2:3:4:5:6:7:8", "https://example.com:80:", "http:///path", "http:////path",
}

func main() {
	for _, s := range inputs {
		u, err := url.Parse(s)
		show("Parse "+q(s), u, err)
		u, err = url.ParseRequestURI(s)
		show("ParseRequestURI "+q(s), u, err)
	}

	base, _ := url.Parse("http://a/b/c/d;p?q")
	for _, ref := range []string{
		"g:h", "g", "./g", "g/", "/g", "//g", "?y", "g?y", "#s", "g#s", "g?y#s", ";x", "g;x", "g;x?y#s", "",
		".", "./", "..", "../", "../g", "../..", "../../", "../../g", "../../../g", "/./g", "/../g", "g.", ".g",
		"g..", "..g", "./../g", "./g/.", "g/./h", "g/../h", "g;x=1/./y", "g;x=1/../y", "g?y/./x", "g#s/../x",
		"http:g", "%zz", "https://other/x/../y",
	} {
		u, err := base.Parse(ref)
		show("Resolve "+q(ref), u, err)
	}
	opaque, _ := url.Parse("mailto:a@b")
	rel, _ := url.Parse("?x=1")
	show("ResolveOpaque", opaque.ResolveReference(rel), nil)

	for _, tc := range [][]string{
		{"https://example.com", "grpc.health.v1.Health", "Check"},
		{"https://idp.example.com/realms/x", ".well-known/openid-configuration"},
		{"https://idp.example.com/realms/x/", "/.well-known/openid-configuration"},
		{"https://go.googlesource.com", "go"},
		{"https://go.googlesource.com/a/b/c", "../../../go"},
		{"https://go.googlesource.com/", "../go"},
		{"https://go.googlesource.com", "../go"},
		{"https://go.googlesource.com/a%2fb", "c%2fd"},
		{"https://go.googlesource.com/", "100%"},
		{"https://go.googlesource.com/", "a/"},
		{"a", "../../b"},
		{"", "../a"},
		{"/", ""},
		{"http://[::1", "x"},
	} {
		got, err := url.JoinPath(tc[0], tc[1:]...)
		if err != nil {
			println("JoinPath", q(tc[0]), "err", q(err.Error()))
		} else {
			println("JoinPath", q(tc[0]), q(got))
		}
		if u, err := url.Parse(tc[0]); err == nil {
			println("URL.JoinPath", q(u.JoinPath(tc[1:]...).String()))
		}
	}

	for _, s := range []string{
		"", "abc", "one two", "10%", " ?&=#+%!<>#\"{}|\\^[]`☺\t:/@$'()*,;", "a/b", "a+b", "€", "\xff",
		"%", "%a", "%zz", "%41%42", "a%2Fb", "a+b%20c", "%25", "%2B", "%%",
	} {
		qu, qerr := url.QueryUnescape(s)
		pu, perr := url.PathUnescape(s)
		println("Escape", q(s), q(url.QueryEscape(s)), q(url.PathEscape(s)), q(qu), errString(qerr), q(pu), errString(perr))
	}

	for _, s := range []string{
		"a=1&b=2", "a=1&a=2&a=banana", "ascii=%3Ckey%3A+0x90%3E", "a=1;b=2", "a;b=1", "a=%zz&b=2", "a=1&b=%zz",
		"&&a&b=&=c", "x=1&x=2&y", "a=1&;", ";a=1&b=2",
	} {
		v, err := url.ParseQuery(s)
		println("ParseQuery", q(s), q(v.Encode()), errString(err), q(v.Get("a")), b(v.Has("b")), strconv.Itoa(len(v["x"])))
	}

	v := url.Values{}
	v.Set("grant_type", "client_credentials")
	v.Add("scope", "a b")
	v.Add("scope", "c&d")
	v.Set("client_id", "id+1")
	v.Add("empty", "")
	println("Values", q(v.Encode()), q(v.Get("scope")), q(v.Get("missing")), b(v.Has("empty")))
	c := v.Clone()
	v.Del("scope")
	v.Set("client_id", "changed")
	println("Values", q(v.Encode()), b(v.Has("scope")), q(c.Encode()))
	var nilValues url.Values
	println("NilValues", q(nilValues.Encode()), q(nilValues.Get("a")), b(nilValues.Has("a")), b(nilValues.Clone() == nil))

	u := &url.URL{Scheme: "https", User: url.UserPassword("me", "s@cret:/"), Host: "kas.example.com:443", Path: "/a b/c", RawQuery: "x=1"}
	println("Built", q(u.String()), q(u.Redacted()), q(u.User.String()), q(u.Hostname()), q(u.Port()))
	cl := u.Clone()
	cl.Host = "other"
	cl.User = url.User("you")
	println("Clone", q(u.String()), q(cl.String()))
	println("Built", q((&url.URL{Scheme: "http", Path: "//host/p", OmitHost: true}).String()))
	println("Built", q((&url.URL{Path: "a:b"}).String()), q((&url.URL{Path: "*"}).RequestURI()))
	println("Built", q((&url.URL{Scheme: "s", Opaque: "//o", RawQuery: "q"}).RequestURI()))
	text, _ := u.MarshalBinary()
	var back url.URL
	println("Binary", q(string(text)), errString(back.UnmarshalBinary(text)), q(back.String()))
	println("Binary", errString(back.UnmarshalBinary([]byte("http://[::1"))))

	_, err := url.Parse("http://[fe80::1%en0]/")
	var ue *url.Error
	println("ErrorsAs", b(errors.As(err, &ue)), b(ue.Timeout()), b(ue.Temporary()), b(errors.Unwrap(err) == ue.Err))
	_, err = url.Parse("http://%zz")
	var ee url.EscapeError
	println("EscapeError", b(errors.As(err, &ee)), q(string(ee)))
	_, err = url.Parse("http://a<b")
	var he url.InvalidHostError
	println("InvalidHostError", b(errors.As(err, &he)), q(string(he)))

	for _, s := range []string{
		"localhost:http", "localhost:80", "[::1]:80", "[::1%lo0]:80", ":80", "golang.org:", "[::1]:",
		"golang.org", "[::1]", "::1", "fe80::1%lo0:80", "[foo:bar]baz", "[foo]bar:baz", "[foo]:[bar]:baz",
		"[foo]:[bar]baz", "foo[bar]:baz", "foo]bar:baz", "", "[", "[]:", "kas.example.com:443",
	} {
		host, port, err := net.SplitHostPort(s)
		var ae *net.AddrError
		isAddr := errors.As(err, &ae)
		println("SplitHostPort", q(s), q(host), q(port), errString(err), b(isAddr))
		println("JoinHostPort", q(net.JoinHostPort(s, "443")))
	}

	kas, _ := url.Parse("https://KAS.example.com/kas")
	port := kas.Port()
	if port == "" {
		port = "443"
	}
	println("Origin", q(kas.Scheme+"://"+net.JoinHostPort(strings.ToLower(kas.Hostname()), port)))
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return q(err.Error())
}

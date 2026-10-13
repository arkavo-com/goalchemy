package url

type encoding uint8

const (
	encodePath encoding = 1 << iota
	encodePathSegment
	encodeHost
	encodeZone
	encodeUserPassword
	encodeQueryComponent
	encodeFragment
)

func shouldEscape(c byte, mode encoding) bool {
	if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' {
		return false
	}

	if mode == encodeHost || mode == encodeZone {
		switch c {
		case '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', ':', '[', ']', '<', '>', '"':
			return false
		}
	}

	switch c {
	case '-', '_', '.', '~':
		return false

	case '$', '&', '+', ',', '/', ':', ';', '=', '?', '@':
		switch mode {
		case encodePath:
			return c == '?'

		case encodePathSegment:
			return c == '/' || c == ';' || c == ',' || c == '?'

		case encodeUserPassword:
			return c == '@' || c == '/' || c == '?' || c == ':'

		case encodeQueryComponent:
			return true

		case encodeFragment:
			return false
		}
	}

	if mode == encodeFragment {
		switch c {
		case '!', '(', ')', '*':
			return false
		}
	}

	return true
}

func ishex(c byte) bool {
	return '0' <= c && c <= '9' ||
		'a' <= c && c <= 'f' ||
		'A' <= c && c <= 'F'
}

package rt

import nativehttp "github.com/eugenioenko/goalchemy/lib/http"

// LibHTTPExchangeWrite sends a copy of data as the next request body chunk.
func LibHTTPExchangeWrite(t *Task, ex *Exchange, data []byte) {
	p := append([]byte(nil), data...)
	exchangeOp(t, ex, func(n *nativehttp.Exchange) []any { return []any{n.Write(p)} })
}

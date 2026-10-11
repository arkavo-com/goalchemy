package rt

import nativehttp "github.com/eugenioenko/goalchemy/lib/http"

// LibHTTPExchangeRead reads the next response body chunk.
func LibHTTPExchangeRead(t *Task, ex *Exchange, max int) {
	exchangeOp(t, ex, func(n *nativehttp.Exchange) []any {
		data, eof, err := n.Read(max)
		return []any{data, eof, err}
	})
}

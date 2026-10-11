package rt

import nativehttp "github.com/eugenioenko/goalchemy/lib/http"

// LibHTTPExchangeResponse waits for the response status and headers.
func LibHTTPExchangeResponse(t *Task, ex *Exchange) {
	exchangeOp(t, ex, func(n *nativehttp.Exchange) []any {
		status, headers, err := n.Response()
		return []any{status, headers, err}
	})
}

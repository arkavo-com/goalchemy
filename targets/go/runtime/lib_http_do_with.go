package rt

// LibHTTPDoWith is LibHTTPDo with transport options.
func LibHTTPDoWith(t *Task, c Context, method, url string, headers []string, body []byte, maxResponseBytes int, timeoutMillis int64, options []string) {
	libHTTPDo(t, c, method, url, headers, body, maxResponseBytes, timeoutMillis, options)
}

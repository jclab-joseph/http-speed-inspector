package tcpawarehttp

import (
	"bufio"
	"fmt"
	"net/http"
)

type responseWriter struct {
	header http.Header
	bufrw  *bufio.ReadWriter
}

func (w *responseWriter) Header() http.Header {
	return w.header
}

func (w *responseWriter) Write(b []byte) (int, error) {
	return w.bufrw.Write(b)
}

func (w *responseWriter) WriteHeader(statusCode int) {
	// Write status line
	_, _ = fmt.Fprintf(w.bufrw, "HTTP/1.1 %d %s\r\n", statusCode, http.StatusText(statusCode))
	// Write headers
	_ = w.header.Write(w.bufrw)
	_, _ = w.bufrw.Write([]byte("\r\n"))
	_ = w.bufrw.Flush()
}

func (w *responseWriter) Flush() {
	_ = w.bufrw.Flush()
}

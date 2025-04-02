package fixedjson

import (
	"bytes"
	"encoding/json"
	"github.com/jclab-joseph/http-speed-inspector/internal/apputil"
	"io"
)

var dummyZero [8192]byte

const DownloadResponseSize = 4096

type DownloadResponse struct {
	// First nano time of start response
	First int64 `json:"first"`
	// Last nano time of end response
	Last         int64 `json:"last"`
	TotalRetrans int   `json:"totalRetrans"`
	SegsOut      int   `json:"segsOut"`
}

type ConsumeBuffer struct {
	TotalSize int

	TotalRead int64

	buf      [1048576]byte
	position int
	footer   []byte
}

func Write(w io.Writer, data interface{}, size int) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err := w.Write(raw); err != nil {
		return err
	}
	remaining := size - len(raw)
	if remaining > 0 {
		if _, err := w.Write(dummyZero[:remaining]); err != nil {
			return err
		}
	}
	return nil
}

func NewConsumeBuffer(totalSize int, footerSize int) *ConsumeBuffer {
	return &ConsumeBuffer{
		TotalSize: totalSize,
		footer:    make([]byte, 0, footerSize),
	}
}

func intMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (c *ConsumeBuffer) Read(r io.Reader) ([]byte, int64, error) {
	var output []byte
	n, err := r.Read(c.buf[:])
	lastReceivedAt := apputil.GetNano()
	if n > 0 {
		c.TotalRead += int64(n)
		dummySize := intMin(c.TotalSize-c.position, n)
		chunk := c.buf[dummySize:n]
		c.position += dummySize

		if len(chunk) > 0 {
			available := intMin(cap(c.footer)-len(c.footer), len(chunk))
			c.footer = append(c.footer, chunk[:available]...)
			chunk = chunk[available:]
			if len(c.footer) == cap(c.footer) {
				output = c.footer[0:cap(c.footer)]
				c.position = 0
				c.footer = c.footer[:0]

				if endAt := bytes.IndexRune(output, 0); endAt >= 0 {
					output = output[:endAt]
				}
			}
		}

		c.position += len(chunk)
	}

	return output, lastReceivedAt, err
}

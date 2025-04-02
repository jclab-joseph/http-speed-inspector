package main

import (
	"flag"
	"fmt"
	"github.com/jclab-joseph/http-speed-inspector/internal/apputil"
	"github.com/jclab-joseph/http-speed-inspector/internal/fixedjson"
	"github.com/jclab-joseph/http-speed-inspector/internal/tcpawarehttp"
	"github.com/jclab-joseph/http-speed-inspector/pkg/tcpinfo"
	"log"
	"net/http"
	"strconv"
)

func main() {
	port := flag.Int("port", 3000, "Server port")
	flag.Parse()

	http.HandleFunc("/api/downloading", func(w http.ResponseWriter, r *http.Request) {
		receivedAt := apputil.GetNano()
		var firstStartAt int64 = 0

		tcpCtx := tcpawarehttp.GetTcpCtx(r.Context())
		connCtx := tcpCtx.GetConnCtx()

		sizeStr := r.URL.Query().Get("size")
		closeStr := r.URL.Query().Get("close")

		size, err := strconv.Atoi(sizeStr)
		if err != nil {
			http.Error(w, "Invalid size parameter", http.StatusBadRequest)
			return
		}

		// Convert MiB to bytes
		rg := apputil.NewFastRand()
		dummySize := size * 1024 * 1024
		buf := make([]byte, 1048576)
		iteration := dummySize / len(buf)

		totalResponseSize := dummySize + fixedjson.DownloadResponseSize

		w.Header().Set("x-app-received-at", fmt.Sprintf("%d", receivedAt))
		w.Header().Set("Content-Type", "application/octet-stream")
		if closeStr == "true" {
			w.Header().Set("Connection", "close")
		}
		w.Header().Set("Content-Length", strconv.Itoa(totalResponseSize))

		for i := 0; i < iteration; i++ {
			// Generate random data
			_, err = rg.Read(buf)
			if err != nil {
				log.Printf("Failed to generate random data: %+v", err)
				return
			}
			if firstStartAt == 0 {
				firstStartAt = apputil.GetNano()
			}
			if _, err := w.Write(buf); err != nil {
				log.Printf("Failed to write random data: %+v", err)
				return
			}
		}

		response := &fixedjson.DownloadResponse{
			First: firstStartAt,
			Last:  apputil.GetNano(),
		}

		tcpInfo, err := tcpinfo.GetTcpInfo(tcpCtx.NativeConn)
		if err != nil {
			log.Printf("GetTcpInfo failed: %+v", err)
		} else {
			curTotalRetrans := tcpInfo.GetTotalRetrans()
			response.TotalRetrans = curTotalRetrans - connCtx.PrevTotalRetrans
			connCtx.PrevTotalRetrans = curTotalRetrans
		}
		if err := fixedjson.Write(w, response, fixedjson.DownloadResponseSize); err != nil {
			log.Printf("Failed to write response: %+v", err)
			return
		}
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Server starting on %s", addr)

	handler := &tcpawarehttp.TcpAwareHandler{
		Handler: http.DefaultServeMux,
	}
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatal(err)
	}
}

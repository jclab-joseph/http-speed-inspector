package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/jclab-joseph/http-speed-inspector/internal/apputil"
	"github.com/jclab-joseph/http-speed-inspector/internal/fixedjson"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

func main() {
	// Parse command line flags
	serverURL := flag.String("server", "", "Target server URL")
	influxURL := flag.String("influx-url", "", "InfluxDB server URL")
	influxToken := flag.String("influx-token", "", "InfluxDB token")
	influxOrg := flag.String("influx-org", "primary", "InfluxDB organization")
	influxBucket := flag.String("influx-bucket", "network-test", "InfluxDB bucket")
	size := flag.Int("size", 10, "Size in MiB")
	flag.Parse()

	// Create InfluxDB client
	client := influxdb2.NewClient(*influxURL, *influxToken)
	defer client.Close()
	writeApi := client.WriteAPI(*influxOrg, *influxBucket)

	hostname, _ := os.Hostname()

	// Create HTTP client with TLS skip verify
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup

	testCtx := &TestContext{
		writeApi: writeApi,
		Client:   hostname,
		Server:   *serverURL,
	}

	wg.Add(2)
	go func() {
		defer wg.Done()

		// Run tests
		log.Println("Starting TEST1 (keep-alive)")
		testCtx.runTest(ctx, httpClient, *serverURL, false, *size, "http/keep-alive")
	}()
	go func() {
		defer wg.Done()

		log.Println("Starting TEST2 (close)")
		testCtx.runTest(ctx, httpClient, *serverURL, true, *size, "http/close")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		t := time.NewTicker(time.Second)
		defer t.Stop()

	loop:
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				break loop
			case <-t.C:
				writeApi.Flush()
			}
		}
	}()

	wg.Wait()
}

type TestContext struct {
	writeApi api.WriteAPI

	Client string
	Server string
}

func (t *TestContext) runTest(ctx context.Context, client *http.Client, serverURL string, close bool, size int, testName string) {
	for ctx.Err() == nil {
		url := fmt.Sprintf("%s/api/downloading?close=%v&size=%d", serverURL, close, size)

		clientSentAt := time.Now()
		resp, err := client.Get(url)
		t4 := apputil.GetNano()
		if err != nil {
			log.Printf("Request failed: %v", err)
			continue
		}
		t.handleSimpleResponse(clientSentAt, t4, resp, size*1024*1024, testName)
	}
}

func (t *TestContext) handleSimpleResponse(clientSentAt time.Time, t4 int64, resp *http.Response, size int, testName string) {
	// t1 [client] t1
	// t2 [server] serverReceivedAt
	// t3 [server] serverFirstSentAt
	// t4 [client] t4
	// t5 [client] complete

	t1 := apputil.ToNano(clientSentAt)

	t2, err := strconv.ParseInt(resp.Header.Get("x-app-received-at"), 10, 64)
	if err != nil {
		log.Printf("parse x-app-received-at failed: %v", err)
		return
	}

	cb := fixedjson.NewConsumeBuffer(size, fixedjson.DownloadResponseSize)
	for {
		output, lastReceivedAt, err := cb.Read(resp.Body)
		if output != nil {
			t5 := apputil.GetNano()
			var data fixedjson.DownloadResponse
			if err := json.Unmarshal(output, &data); err != nil {
				log.Printf("unmarshal failed: %v", err)
				return
			}

			t3 := data.First
			latencySec := float64((t4-t1)-(t3-t2)) / 2.0 / 1000000000.0

			duration := float64(t5-t1) / 1000000000.0
			bps := int64(float64(size*8) / float64(duration))
			//log.Printf("DONE: duration=%f s, latency=%f us", duration, latencyUs)
			//log.Printf("\tbps : %f mbps", mbps)
			//log.Printf("\tt4-t1 : %d", t4-t1)
			//log.Printf("\tt3-t2 : %d", t3-t2)
			//log.Printf("\tTotalRead : %d", cb.TotalRead)

			logMeta := &LogMeta{
				Server: t.Server,
				Client: t.Client,
				Size:   size,
				Name:   testName,
			}
			logData := &LogData{
				Duration: duration,
				Bps:      bps,
				Latency:  latencySec,
			}

			point := influxdb2.NewPoint(
				"http",
				logMeta.ToMap(),
				logData.ToMap(),
				clientSentAt,
			)
			t.writeApi.WritePoint(point)

			log.Printf("[%s] duration=%.3fs, latency=%.4fs, bps=%.3f Mbps", testName, duration, latencySec, float64(bps)/1000000)

			_ = lastReceivedAt
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("read failed: %+v", err)
			}
			break
		}
	}
}

type LogMeta struct {
	Client string `json:"client"`
	Server string `json:"server"`
	Size   int    `json:"size"`
	Name   string `json:"name"`
}

func (m *LogMeta) ToMap() map[string]string {
	return map[string]string{
		"client": m.Client,
		"server": m.Server,
		"size":   strconv.Itoa(m.Size),
		"name":   m.Name,
	}
}

type LogData struct {
	Duration float64 `json:"duration"`
	Latency  float64 `json:"latency"`
	Bps      int64   `json:"bps"`
}

func (m *LogData) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"duration": m.Duration,
		"latency":  m.Latency,
		"bps":      m.Bps,
	}
}

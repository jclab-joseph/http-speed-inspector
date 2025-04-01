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
	"github.com/jclab-joseph/http-speed-inspector/internal/testmetric"
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
	timeout := flag.Int("timeout", 60, "timeout seconds")
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
		ctx:        ctx,
		httpClient: httpClient,
		writeApi:   writeApi,
		Client:     hostname,
		Server:     *serverURL,
		errorStats: testmetric.NewErrorStats(),
		Timeout:    time.Duration(*timeout) * time.Second,
	}
	basicMeta := map[string]string{
		"client": testCtx.Client,
		"server": testCtx.Server,
	}
	testCtx.errorStats.RegisterTest("http/keep-alive", "http/close")

	wg.Add(2)
	go func() {
		defer wg.Done()

		// Run tests
		log.Println("Starting TEST1 (keep-alive)")
		testCtx.testWorker("http/keep-alive", false, *size)
	}()
	go func() {
		defer wg.Done()

		log.Println("Starting TEST2 (close)")
		testCtx.testWorker("http/close", true, *size)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(time.Second * 10)
		defer ticker.Stop()

	loop:
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				break loop
			case <-ticker.C:
				point := testCtx.errorStats.GetPointAndReset(
					"error_count",
					basicMeta,
				)
				writeApi.WritePoint(point)
				writeApi.Flush()
			}
		}
	}()

	wg.Wait()
}

type TestContext struct {
	ctx        context.Context
	httpClient *http.Client
	writeApi   api.WriteAPI
	errorStats *testmetric.ErrorStats

	Client  string
	Server  string
	Timeout time.Duration
}

func (t *TestContext) testWorker(testName string, isClose bool, size int) {
	for t.ctx.Err() == nil {
		t.testOnce(testName, isClose, size)
	}
}

func (t *TestContext) testOnce(testName string, isClose bool, sizeMb int) {
	url := fmt.Sprintf("%s/api/downloading?close=%v&size=%d", t.Server, isClose, sizeMb)
	size := sizeMb * 1048576

	reqCtx, cancel := context.WithTimeout(t.ctx, t.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		log.Printf("ERROR: %+v", err)
	}
	if isClose {
		req.Header.Set("Connection", "close")
	}

	clientSentAt := time.Now()
	resp, err := t.httpClient.Do(req)
	t4 := apputil.GetNano()
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.errorStats.IncrementError(testName)
		}
		log.Printf("Request failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[%s] Request failed: code=%d", testName, resp.StatusCode)
		t.errorStats.IncrementError(testName)
		return
	}

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

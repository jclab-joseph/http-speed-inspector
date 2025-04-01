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
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
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

	logEnabled := flag.Bool("log-enabled", false, "Enable logging to file")
	logPath := flag.String("log-path", "speed-inspector.log", "Log file path")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")

	flag.Parse()

	// zap logging initialize
	logger, err := initLogger(*logEnabled, *logPath, *logLevel)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()
	zap.ReplaceGlobals(logger)

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
		log:        logger.Sugar(),
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
		testCtx.log.Infof("Starting TEST1 (keep-alive)")
		testCtx.testWorker("http/keep-alive", false, *size)
	}()
	go func() {
		defer wg.Done()

		testCtx.log.Infof("Starting TEST2 (close)")
		testCtx.testWorker("http/close", true, *size)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(time.Second * 60)
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

func initLogger(logEnabled bool, logPath string, logLevel string) (*zap.Logger, error) {
	// 기본 인코더 설정
	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder

	// Console은 항상 Debug 레벨
	consoleLevel := zap.NewAtomicLevelAt(zapcore.DebugLevel)

	// File log level 설정
	fileLevel := zap.NewAtomicLevel()
	switch logLevel {
	case "debug":
		fileLevel.SetLevel(zapcore.DebugLevel)
	case "info":
		fileLevel.SetLevel(zapcore.InfoLevel)
	case "warn":
		fileLevel.SetLevel(zapcore.WarnLevel)
	case "error":
		fileLevel.SetLevel(zapcore.ErrorLevel)
	default:
		fileLevel.SetLevel(zapcore.InfoLevel)
	}

	// 콘솔 출력 설정
	consoleEncoder := zapcore.NewConsoleEncoder(encoderConfig)
	consoleSyncer := zapcore.AddSync(os.Stdout)

	var cores []zapcore.Core
	cores = append(cores, zapcore.NewCore(consoleEncoder, consoleSyncer, consoleLevel))

	// 파일 로깅이 활성화된 경우 파일 출력 설정 추가
	if logEnabled {
		fileEncoder := zapcore.NewConsoleEncoder(encoderConfig)
		fileHandle, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("cannot open log file: %v", err)
		}
		fileSyncer := zapcore.AddSync(fileHandle)
		cores = append(cores, zapcore.NewCore(fileEncoder, fileSyncer, fileLevel))
	}

	// 모든 코어를 결합
	core := zapcore.NewTee(cores...)
	logger := zap.New(core)

	return logger, nil
}

type TestContext struct {
	ctx        context.Context
	httpClient *http.Client
	writeApi   api.WriteAPI
	errorStats *testmetric.ErrorStats
	log        *zap.SugaredLogger

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
		t.log.Warnf("ERROR: %+v", err)
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
		t.log.Warnf("[%s] Request failed: %+v", testName, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.log.Warnf("[%s] Request failed: code=%d", testName, resp.StatusCode)
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
		t.log.Warnf("parse x-app-received-at failed: %v", err)
		return
	}

	cb := fixedjson.NewConsumeBuffer(size, fixedjson.DownloadResponseSize)
	for {
		output, lastReceivedAt, err := cb.Read(resp.Body)
		if output != nil {
			t5 := apputil.GetNano()
			var data fixedjson.DownloadResponse
			if err := json.Unmarshal(output, &data); err != nil {
				t.log.Warnf("unmarshal failed: %v", err)
				return
			}

			t3 := data.First
			latencySec := float64((t4-t1)-(t3-t2)) / 2.0 / 1000000000.0

			duration := float64(t5-t1) / 1000000000.0
			bps := int64(float64(size*8) / float64(duration))
			//t.log.Warnf("DONE: duration=%f s, latency=%f us", duration, latencyUs)
			//t.log.Warnf("\tbps : %f mbps", mbps)
			//t.log.Warnf("\tt4-t1 : %d", t4-t1)
			//t.log.Warnf("\tt3-t2 : %d", t3-t2)
			//t.log.Warnf("\tTotalRead : %d", cb.TotalRead)

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

			t.log.Debugf("[%s] duration=%.3fs, latency=%.4fs, bps=%.3f Mbps", testName, duration, latencySec, float64(bps)/1000000)

			_ = lastReceivedAt
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.log.Warnf("[%s] Read failed: %+v", testName, err)
				t.errorStats.IncrementError(testName)
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

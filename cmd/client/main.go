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
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	// Parse command line flags
	serverURL := flag.String("server", "http://127.0.0.1:3000", "Target server URL")
	influxURL := flag.String("influx-url", "", "InfluxDB server URL")
	influxToken := flag.String("influx-token", "", "InfluxDB token")
	influxOrg := flag.String("influx-org", "primary", "InfluxDB organization")
	influxBucket := flag.String("influx-bucket", "network-test", "InfluxDB bucket")
	size := flag.Int("size", 10, "Size in MiB")
	timeout := flag.Int("timeout", 60, "timeout seconds")
	flushTime := flag.Int("flush-time", 60, "flush time")

	logEnabled := flag.Bool("log-enabled", false, "Enable logging to file")
	logPath := flag.String("log-path", "speed-inspector.log", "Log file path")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")

	testsFlag := flag.String("tests", "http/close,http/keep-alive,http/connect,tls1.2/connect,tls1.3/connect", "")
	tlsVersion := flag.String("tls-version", "", "1.2 or 1.3")
	flag.Parse()

	tests := make(map[string]bool)
	testList := strings.Split(*testsFlag, ",")
	for _, v := range testList {
		tests[v] = true
	}

	// zap logging initialize
	logger, err := initLogger(*logEnabled, *logPath, *logLevel)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer logger.Sync()
	zap.ReplaceGlobals(logger)

	hostname, _ := os.Hostname()

	// Create HTTP client with TLS skip verify
	defaultTlsConfig := &tls.Config{InsecureSkipVerify: true}
	switch *tlsVersion {
	case "1.2":
		defaultTlsConfig.MinVersion = tls.VersionTLS12
		defaultTlsConfig.MaxVersion = tls.VersionTLS12
	case "1.3":
		defaultTlsConfig.MinVersion = tls.VersionTLS13
		defaultTlsConfig.MaxVersion = tls.VersionTLS13
	case "":
	default:
		log.Fatalf("unknown tlsVersion: %s", *tlsVersion)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: defaultTlsConfig,
		},
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var wg sync.WaitGroup

	testCtx := &TestContext{
		ctx:          ctx,
		log:          logger.Sugar(),
		httpClient:   httpClient,
		Client:       hostname,
		Server:       *serverURL,
		successStats: testmetric.NewCounterStat(),
		errorStats:   testmetric.NewCounterStat(),
		Timeout:      time.Duration(*timeout) * time.Second,
	}

	var client influxdb2.Client
	if len(*influxURL) > 0 {
		// Create InfluxDB client
		client = influxdb2.NewClient(*influxURL, *influxToken)
		defer client.Close()
		testCtx.writeApi = client.WriteAPI(*influxOrg, *influxBucket)
	}

	basicMeta := map[string]string{
		"client": testCtx.Client,
		"server": testCtx.Server,
	}
	testCtx.successStats.RegisterTest(testList...)
	testCtx.errorStats.RegisterTest(testList...)

	if tests["http/keep-alive"] {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Run tests
			testName := "http/keep-alive"
			if len(*tlsVersion) > 0 {
				testName += "/tls" + *tlsVersion
			}
			testCtx.log.Infof("Starting TEST: %s", testName)
			testCtx.httpDownloadTestWorker(testName, false, *size)
		}()
	}
	if tests["http/close"] {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Run tests
			testName := "http/keep-close"
			if len(*tlsVersion) > 0 {
				testName += "/tls" + *tlsVersion
			}
			testCtx.log.Infof("Starting TEST: %s", testName)
			testCtx.httpDownloadTestWorker(testName, true, *size)
		}()
	}
	if tests["http/connect"] {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Run tests
			testName := "http/connect"
			if len(*tlsVersion) > 0 {
				testName += "/tls" + *tlsVersion
			}
			testCtx.log.Infof("Starting TEST: %s", testName)
			testCtx.httpConnectTestWorker(testName, defaultTlsConfig)
		}()
	}
	if tests["tls1.2/connect"] {
		wg.Add(1)
		go func() {
			defer wg.Done()

			testCtx.log.Infof("Starting tls1.2/connect")
			tlsConfig := &tls.Config{
				MinVersion: tls.VersionTLS12,
				MaxVersion: tls.VersionTLS12,
			}
			testCtx.httpConnectTestWorker("tls1.2/connect", tlsConfig)
		}()
	}
	if tests["tls1.3/connect"] {
		wg.Add(1)
		go func() {
			defer wg.Done()

			testCtx.log.Infof("Starting tls1.3/connect")
			tlsConfig := &tls.Config{
				MinVersion: tls.VersionTLS13,
				MaxVersion: tls.VersionTLS13,
			}
			testCtx.httpConnectTestWorker("tls1.3/connect", tlsConfig)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(time.Second * time.Duration(*flushTime))
		defer ticker.Stop()

	loop:
		for ctx.Err() == nil {
			select {
			case <-ctx.Done():
				break loop
			case <-ticker.C:
				successCounts := testCtx.successStats.GetCountsAndReset()
				errorCounts := testCtx.errorStats.GetCountsAndReset()

				if testCtx.writeApi != nil {
					testCtx.writeApi.WritePoint(influxdb2.NewPoint(
						"success_count",
						basicMeta,
						successCounts,
						time.Now(),
					))
					testCtx.writeApi.WritePoint(influxdb2.NewPoint(
						"error_count",
						basicMeta,
						errorCounts,
						time.Now(),
					))
					testCtx.writeApi.Flush()
				} else {
					log.Printf("successCountPoint: %+v", successCounts)
					log.Printf("errorCountPoint: %+v", errorCounts)
				}
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
	ctx          context.Context
	httpClient   *http.Client
	writeApi     api.WriteAPI
	successStats *testmetric.CounterStat
	errorStats   *testmetric.CounterStat
	log          *zap.SugaredLogger

	Client  string
	Server  string
	Timeout time.Duration
}

func (t *TestContext) httpDownloadTestWorker(testName string, isClose bool, size int) {
	for t.ctx.Err() == nil {
		t.httpDownloadTestOnce(testName, isClose, size)
	}
}

func (t *TestContext) httpDownloadTestOnce(testName string, isClose bool, sizeMb int) {
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
			t.errorStats.Increment(testName)
		}
		t.log.Warnf("[%s] Request failed: %+v", testName, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.log.Warnf("[%s] Request failed: code=%d", testName, resp.StatusCode)
		t.errorStats.Increment(testName)
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

			t.successStats.Increment(testName)

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
				Duration:               duration,
				Bps:                    bps,
				Latency:                latencySec,
				TotalRetrans:           data.TotalRetrans,
				SegsOut:                data.SegsOut,
				TotalRetransPercentage: float64(data.TotalRetrans) / float64(data.SegsOut) * 100.0,
			}

			if t.writeApi != nil {
				t.writeApi.WritePoint(influxdb2.NewPoint(
					"http",
					logMeta.ToMap(),
					logData.ToMap(),
					clientSentAt,
				))
			} else {
				log.Printf("[%s] http point: %+v", testName, logData.ToMap())
			}

			t.log.Debugf("[%s] duration=%.3fs, latency=%.4fs, bps=%.3f Mbps", testName, duration, latencySec, float64(bps)/1000000)

			_ = lastReceivedAt
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.log.Warnf("[%s] Read failed: %+v", testName, err)
				t.errorStats.Increment(testName)
			}
			break
		}
	}
}

func (t *TestContext) httpConnectTestWorker(testName string, tlsConfig *tls.Config) {
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   tlsConfig,
			DisableKeepAlives: true,
		},
	}
	for t.ctx.Err() == nil {
		t.httpConnectTestOnce(testName, httpClient)
	}
}

func (t *TestContext) httpConnectTestOnce(testName string, httpClient *http.Client) {
	url := fmt.Sprintf("%s/api/ping", t.Server)

	reqCtx, cancel := context.WithTimeout(t.ctx, t.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		t.log.Warnf("ERROR: %+v", err)
		return
	}
	req.Header.Set("Connection", "close")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			t.errorStats.Increment(testName)
		}
		t.log.Warnf("[%s] Request failed: %+v", testName, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.log.Warnf("[%s] Request failed: code=%d", testName, resp.StatusCode)
		t.errorStats.Increment(testName)
		return
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.log.Warnf("[%s] Response read failed: %+v", testName, err)
		t.errorStats.Increment(testName)
		return
	}
	_ = respBody

	t.successStats.Increment(testName)

	t.log.Infof("[%s] success", testName)
	time.Sleep(time.Millisecond * 10)
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
	Duration               float64 `json:"duration"`
	Latency                float64 `json:"latency"`
	Bps                    int64   `json:"bps"`
	TotalRetrans           int     `json:"total_retrans"`
	SegsOut                int     `json:"segs_out"`
	TotalRetransPercentage float64 `json:"total_retrans_percentage"`
}

func (m *LogData) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"duration":                 m.Duration,
		"latency":                  m.Latency,
		"bps":                      m.Bps,
		"total_retrans":            m.TotalRetrans,
		"segs_out":                 m.SegsOut,
		"total_retrans_percentage": m.TotalRetransPercentage,
	}
}

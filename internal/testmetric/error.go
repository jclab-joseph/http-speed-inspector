package testmetric

import (
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"sync"
	"time"
)

type ErrorStats struct {
	counters map[string]int
	mu       sync.Mutex
}

func NewErrorStats() *ErrorStats {
	return &ErrorStats{
		counters: make(map[string]int),
	}
}

func (e *ErrorStats) RegisterTest(testNames ...string) {
	for _, testName := range testNames {
		e.counters[testName] = 0
	}
}

func (e *ErrorStats) IncrementError(testName string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.counters[testName]++
}

func (e *ErrorStats) GetCountsAndReset() map[string]interface{} {
	output := make(map[string]interface{})
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range e.counters {
		output[k] = v
		e.counters[k] = 0
	}
	return output
}
func (e *ErrorStats) GetPointAndReset(measurement string, tags map[string]string) *write.Point {
	counters := e.GetCountsAndReset()
	return influxdb2.NewPoint(
		measurement,
		tags,
		counters,
		time.Now(),
	)
}

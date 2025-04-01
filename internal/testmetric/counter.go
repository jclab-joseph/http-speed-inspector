package testmetric

import (
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"sync"
	"time"
)

type CounterStat struct {
	counters map[string]int
	mu       sync.Mutex
}

func NewCounterStat() *CounterStat {
	return &CounterStat{
		counters: make(map[string]int),
	}
}

func (e *CounterStat) RegisterTest(testNames ...string) {
	for _, testName := range testNames {
		e.counters[testName] = 0
	}
}

func (e *CounterStat) IncrementError(testName string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.counters[testName]++
}

func (e *CounterStat) GetCountsAndReset() map[string]interface{} {
	output := make(map[string]interface{})
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, v := range e.counters {
		output[k] = v
		e.counters[k] = 0
	}
	return output
}
func (e *CounterStat) GetPointAndReset(measurement string, tags map[string]string) *write.Point {
	counters := e.GetCountsAndReset()
	return influxdb2.NewPoint(
		measurement,
		tags,
		counters,
		time.Now(),
	)
}

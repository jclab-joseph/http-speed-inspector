package apputil

import "time"

var baseTime = time.Now()

func GetNano() int64 {
	return time.Now().Sub(baseTime).Nanoseconds()
}

func ToNano(t time.Time) int64 {
	return t.Sub(baseTime).Nanoseconds()
}

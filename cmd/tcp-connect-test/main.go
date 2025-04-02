package main

import (
	"context"
	"log"
	"net"
	"time"
)

var success int
var failure int

func main() {
	var i int
	for {
		dial()
		i++
		if i%10 == 0 {
			log.Printf("count: success=%d, failure=%d, total=%d", success, failure, i)
		}
		time.Sleep(time.Millisecond * 100)
	}
}

var nextPort = 10000
var dialer = &net.Dialer{}

func dial() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	conn, err := dialer.DialContext(ctx, "tcp4", "10.252.93.39:7777")
	if err != nil {
		failure++
		log.Printf("ERROR: %+v", err)
	} else {
		success++
		log.Printf("success %+v", conn.LocalAddr().String())
		conn.Close()
	}
}

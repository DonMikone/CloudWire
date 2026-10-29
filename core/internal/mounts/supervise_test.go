package mounts

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestRecordFailureReportsOncePerStreak(t *testing.T) {
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	r := &run{}
	backoff := time.Second
	for i := range 4 {
		if r.recordFailure(t0.Add(time.Duration(i)*10*time.Second), &backoff) {
			t.Fatalf("failure %d reported", i+1)
		}
	}
	if !r.recordFailure(t0.Add(50*time.Second), &backoff) {
		t.Fatal("5th failure not reported")
	}
	if r.recordFailure(t0.Add(55*time.Second), &backoff) {
		t.Fatal("6th failure reported again")
	}

	// Mounted for 5 minutes: a new streak.
	backoff = 30 * time.Second
	r.mountedAt = t0.Add(time.Minute)
	if r.recordFailure(t0.Add(6*time.Minute), &backoff) {
		t.Fatal("first failure of a new streak reported")
	}
	if len(r.failures) != 1 || backoff != time.Second || r.reported {
		t.Fatalf("streak not reset: %d failures, backoff %v, reported %v", len(r.failures), backoff, r.reported)
	}

	// Failures 3 minutes apart never reach 5 within 10 minutes.
	r = &run{}
	for i := range 10 {
		if r.recordFailure(t0.Add(time.Duration(i)*3*time.Minute), &backoff) {
			t.Fatalf("spread failure %d reported", i+1)
		}
	}
}

func TestPortFree(t *testing.T) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if portFree(port) {
		t.Fatal("port in use reported free")
	}
	_ = l.Close()
	if !portFree(port) {
		t.Fatal("closed port reported taken")
	}
}

func TestLogDamper(t *testing.T) {
	t0 := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	d := &logDamper{window: time.Hour}
	if !d.allow("a", t0) {
		t.Fatal("first denied")
	}
	if d.allow("a", t0.Add(59*time.Minute)) {
		t.Fatal("repeat within the window allowed")
	}
	if !d.allow("a", t0.Add(60*time.Minute)) {
		t.Fatal("repeat after the window denied")
	}
	if !d.allow("b", t0.Add(60*time.Minute)) {
		t.Fatal("other key denied")
	}

	d = &logDamper{window: time.Hour}
	for i := range 1024 {
		d.allow(fmt.Sprint(i), t0)
	}
	if !d.allow("new", t0.Add(2*time.Hour)) || len(d.last) != 1 {
		t.Fatalf("expired entries not pruned: %d left", len(d.last))
	}
}

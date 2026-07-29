package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type report struct {
	Mode        string  `json:"mode"`
	Label       string  `json:"label"`
	Target      string  `json:"target"`
	Seconds     float64 `json:"seconds"`
	Bytes       int64   `json:"bytes,omitempty"`
	MBPerSecond float64 `json:"mb_per_second,omitempty"`
	Requests    int64   `json:"requests,omitempty"`
	QPS         float64 `json:"qps,omitempty"`
	Errors      int64   `json:"errors"`
	P50ms       float64 `json:"p50_ms,omitempty"`
	P99ms       float64 `json:"p99_ms,omitempty"`
	P999ms      float64 `json:"p999_ms,omitempty"`
}

func main() {
	var (
		mode        = flag.String("mode", "throughput", "throughput, latency or coldstart")
		target      = flag.String("target", "", "host:port of the tunnel entrypoint")
		label       = flag.String("label", "", "name for this run, echoed in the report")
		duration    = flag.Duration("duration", 10*time.Second, "how long to run")
		concurrency = flag.Int("concurrency", 32, "connections for latency mode")
		payload     = flag.Int("payload", 64, "request size in bytes for latency mode")
		chunk       = flag.Int("chunk", 256<<10, "write size for throughput mode")
		warmup      = flag.Duration("warmup", 2*time.Second, "ignored settling period")
		connectWait = flag.Duration("connect-wait", 60*time.Second, "how long to wait for the target to accept")
		httpHost    = flag.String("http-host", "", "send an HTTP GET with this Host header (coldstart mode)")
		useTLS      = flag.Bool("tls", false, "wrap each connection in TLS, as a browser does (coldstart mode)")
	)
	flag.Parse()

	if *target == "" {
		fmt.Fprintln(os.Stderr, "loadgen: -target is required")
		os.Exit(2)
	}

	if err := waitForTarget(*target, *connectWait); err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: %v\n", err)
		os.Exit(1)
	}

	var (
		result report
		err    error
	)
	switch *mode {
	case "throughput":
		result, err = runThroughput(*target, *duration, *warmup, *chunk)
	case "latency":
		result, err = runLatency(*target, *duration, *warmup, *concurrency, *payload)
	case "coldstart":
		result, err = runColdStart(*target, *duration, *warmup, *concurrency, *httpHost, *useTLS)
	default:
		fmt.Fprintf(os.Stderr, "loadgen: unknown mode %q\n", *mode)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "loadgen: %v\n", err)
		os.Exit(1)
	}

	result.Mode = *mode
	result.Label = *label
	result.Target = *target

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(result)
}

func waitForTarget(target string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var lastErr error

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", target, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("target %s did not accept within %s: %w", target, limit, lastErr)
}

func runThroughput(target string, duration, warmup time.Duration, chunk int) (report, error) {
	conn, err := net.DialTimeout("tcp", target, 10*time.Second)
	if err != nil {
		return report{}, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()

	buf := make([]byte, chunk)
	for i := range buf {
		buf[i] = byte(i)
	}

	var (
		counted atomic.Int64
		errs    atomic.Int64
		measure atomic.Bool
	)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		sink := make([]byte, chunk)
		for {
			n, err := conn.Read(sink)
			if measure.Load() {
				counted.Add(int64(n))
			}
			if err != nil {
				return
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()

	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := conn.Write(buf); err != nil {

				if measure.Load() {
					errs.Add(1)
				}
				return
			}
		}
	}()

	time.Sleep(warmup)
	counted.Store(0)
	measure.Store(true)
	start := time.Now()

	time.Sleep(duration)
	elapsed := time.Since(start)
	measure.Store(false)

	close(stop)
	conn.Close()
	<-writeDone
	wg.Wait()

	bytes := counted.Load()
	seconds := elapsed.Seconds()

	return report{
		Seconds:     round(seconds, 3),
		Bytes:       bytes,
		MBPerSecond: round(float64(bytes)/(1024*1024)/seconds, 2),
		Errors:      errs.Load(),
	}, nil
}

func runLatency(target string, duration, warmup time.Duration, concurrency, payload int) (report, error) {
	var (
		latencies   = make([][]time.Duration, concurrency)
		requests    atomic.Int64
		errs        atomic.Int64
		measure     atomic.Bool
		wg          sync.WaitGroup
		stop        = make(chan struct{})
		requestBody = make([]byte, payload)
	)
	for i := range requestBody {
		requestBody[i] = 'x'
	}

	for worker := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()

			local := make([]time.Duration, 0, 4096)
			defer func() { latencies[worker] = local }()

			conn, err := net.DialTimeout("tcp", target, 10*time.Second)
			if err != nil {
				errs.Add(1)
				return
			}
			defer conn.Close()

			echo := make([]byte, payload)
			for {
				select {
				case <-stop:
					return
				default:
				}

				started := time.Now()
				if _, err := conn.Write(requestBody); err != nil {
					errs.Add(1)
					return
				}
				if _, err := io.ReadFull(conn, echo); err != nil {
					errs.Add(1)
					return
				}
				elapsed := time.Since(started)

				if measure.Load() {
					local = append(local, elapsed)
					requests.Add(1)
				}
			}
		}()
	}

	time.Sleep(warmup)
	requests.Store(0)
	measure.Store(true)
	start := time.Now()

	time.Sleep(duration)
	elapsed := time.Since(start)
	measure.Store(false)
	close(stop)
	wg.Wait()

	var all []time.Duration
	for _, batch := range latencies {
		all = append(all, batch...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	total := requests.Load()
	return report{
		Seconds:  round(elapsed.Seconds(), 3),
		Requests: total,
		QPS:      round(float64(total)/elapsed.Seconds(), 1),
		Errors:   errs.Load(),
		P50ms:    percentileMillis(all, 0.50),
		P99ms:    percentileMillis(all, 0.99),
		P999ms:   percentileMillis(all, 0.999),
	}, nil
}

func percentileMillis(sorted []time.Duration, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(q*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return round(float64(sorted[idx].Microseconds())/1000, 3)
}

func round(v float64, places int) float64 {
	scale := math.Pow(10, float64(places))
	return math.Round(v*scale) / scale
}

func runColdStart(target string, duration, warmup time.Duration,
	concurrency int, httpHost string, useTLS bool) (report, error) {

	var (
		latencies = make([][]time.Duration, concurrency)
		requests  atomic.Int64
		errs      atomic.Int64
		measure   atomic.Bool
		wg        sync.WaitGroup
		stop      = make(chan struct{})
	)

	request := []byte("ping")
	if httpHost != "" {
		request = []byte("GET / HTTP/1.1\r\nHost: " + httpHost +
			"\r\nConnection: close\r\nUser-Agent: openfrp-loadgen\r\n\r\n")
	}

	for worker := range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()

			local := make([]time.Duration, 0, 4096)
			defer func() { latencies[worker] = local }()

			reply := make([]byte, 128)
			for {
				select {
				case <-stop:
					return
				default:
				}

				started := time.Now()

				conn, err := net.DialTimeout("tcp", target, 15*time.Second)
				if err != nil {
					errs.Add(1)
					continue
				}
				conn.SetDeadline(time.Now().Add(15 * time.Second))

				if useTLS {
					tlsConn := tls.Client(conn, &tls.Config{
						ServerName: httpHost,

						InsecureSkipVerify: true,
					})
					if err := tlsConn.Handshake(); err != nil {
						conn.Close()
						errs.Add(1)
						continue
					}
					conn = tlsConn
				}

				if _, err := conn.Write(request); err != nil {
					conn.Close()
					errs.Add(1)
					continue
				}

				n, err := conn.Read(reply)
				if err != nil {
					conn.Close()
					errs.Add(1)
					continue
				}
				elapsed := time.Since(started)
				conn.Close()

				if httpHost != "" && !bytes.Contains(reply[:n], []byte(" 200 ")) {
					errs.Add(1)
					continue
				}

				if measure.Load() {
					local = append(local, elapsed)
					requests.Add(1)
				}
			}
		}()
	}

	time.Sleep(warmup)
	measure.Store(true)
	started := time.Now()

	time.Sleep(duration)
	measure.Store(false)
	elapsed := time.Since(started)
	close(stop)
	wg.Wait()

	var all []time.Duration
	for _, batch := range latencies {
		all = append(all, batch...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })

	result := report{
		Mode:     "coldstart",
		Target:   target,
		Seconds:  elapsed.Seconds(),
		Requests: requests.Load(),
		Errors:   errs.Load(),
	}
	if result.Seconds > 0 {
		result.QPS = float64(result.Requests) / result.Seconds
	}
	if len(all) > 0 {
		result.P50ms = percentileMillis(all, 0.50)
		result.P99ms = percentileMillis(all, 0.99)
		result.P999ms = percentileMillis(all, 0.999)
	}
	return result, nil
}

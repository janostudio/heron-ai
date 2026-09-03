// Command load is a dependency-free HTTP load generator for the Heron engine
// HTTP server (`bin/heron --serve`). It measures throughput and latency of the
// /api/run and /api/sessions/turn endpoints only; memory observation is out of
// scope (watch RSS with a separate terminal, see README.md).
//
// Usage:
//
//	go run ./scripts/bench -concurrency 8 -rounds 10 -base http://127.0.0.1:8080
//
// Each worker goroutine starts a fresh session with /api/run, then drives the
// remaining rounds through /api/sessions/turn?session_id=... to exercise the
// session-continuation path.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

func main() {
	concurrency := flag.Int("concurrency", 8, "number of concurrent worker goroutines")
	rounds := flag.Int("rounds", 10, "requests per worker (first is /api/run, rest are /api/sessions/turn)")
	base := flag.String("base", "http://127.0.0.1:8080", "HTTP server base URL")
	flag.Parse()

	if *concurrency < 1 {
		fmt.Fprintln(os.Stderr, "concurrency must be >= 1")
		os.Exit(2)
	}
	if *rounds < 1 {
		fmt.Fprintln(os.Stderr, "rounds must be >= 1")
		os.Exit(2)
	}

	client := &http.Client{Timeout: 120 * time.Second}

	start := time.Now()
	latencies := make([]time.Duration, 0, *concurrency**rounds)
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errCount int64

	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			sessionID := ""
			for r := 0; r < *rounds; r++ {
				beg := time.Now()
				var err error
				if r == 0 {
					sessionID, err = startRun(client, *base, worker)
				} else {
					err = continueTurn(client, *base, sessionID, worker, r)
				}
				lat := time.Since(beg)
				mu.Lock()
				latencies = append(latencies, lat)
				mu.Unlock()
				if err != nil {
					errCount++
					fmt.Fprintf(os.Stderr, "worker %d round %d: %v\n", worker, r, err)
				}
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)
	total := len(latencies)
	qps := float64(total) / elapsed.Seconds()

	fmt.Printf("=== Heron HTTP load summary ===\n")
	fmt.Printf("concurrency:     %d\n", *concurrency)
	fmt.Printf("rounds/worker:   %d\n", *rounds)
	fmt.Printf("total requests:  %d\n", total)
	fmt.Printf("errors:          %d\n", errCount)
	fmt.Printf("elapsed:         %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("qps:             %.2f req/s\n", qps)
	fmt.Printf("latency p50:     %s\n", percentile(latencies, 0.50).Round(time.Microsecond))
	fmt.Printf("latency p95:     %s\n", percentile(latencies, 0.95).Round(time.Microsecond))
	fmt.Printf("latency p99:     %s\n", percentile(latencies, 0.99).Round(time.Microsecond))
}

// startRun POSTs /api/run and returns the new flow session id.
func startRun(client *http.Client, base string, worker int) (string, error) {
	body := map[string]any{
		"flow_id": "default",
		"input":   fmt.Sprintf("load-test worker %d round 0", worker),
	}
	resp, err := postJSON(client, base+"/api/run", body)
	if err != nil {
		return "", err
	}
	var result struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("decode /api/run response: %w", err)
	}
	if result.Session.ID == "" {
		return "", fmt.Errorf("/api/run returned no session id")
	}
	return result.Session.ID, nil
}

// continueTurn POSTs /api/sessions/turn?session_id=... to continue a session.
func continueTurn(client *http.Client, base, sessionID string, worker, round int) error {
	body := map[string]any{
		"input": fmt.Sprintf("load-test worker %d round %d", worker, round),
	}
	_, err := postJSON(client, base+"/api/sessions/turn?session_id="+sessionID, body)
	return err
}

// postJSON performs a POST with a JSON body and returns the raw response body.
// A non-2xx status is treated as an error.
func postJSON(client *http.Client, url string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s returned %d: %s", url, resp.StatusCode, buf.String())
	}
	return buf.Bytes(), nil
}

// percentile returns the p-th latency percentile (0 <= p <= 1) using the
// nearest-rank method.
func percentile(latencies []time.Duration, p float64) time.Duration {
	if len(latencies) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(latencies))
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

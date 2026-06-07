package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type summary struct {
	Mode          string   `json:"mode"`
	DurationSec   float64  `json:"duration_sec"`
	Workers       int      `json:"workers"`
	Success       int64    `json:"success"`
	Failed        int64    `json:"failed"`
	Throughput    float64  `json:"throughput"`
	ErrorRate     float64  `json:"error_rate"`
	P50Ms         float64  `json:"p50_ms"`
	P95Ms         float64  `json:"p95_ms"`
	P99Ms         float64  `json:"p99_ms"`
	MinMs         float64  `json:"min_ms"`
	MaxMs         float64  `json:"max_ms"`
	Samples       int      `json:"samples"`
	StartedAt     string   `json:"started_at"`
	FinishedAt    string   `json:"finished_at"`
	ErrorsPreview []string `json:"errors_preview,omitempty"`
}

type workerResult struct {
	latencies []int64
	errors    []string
}

func main() {
	var (
		mode        = flag.String("mode", "read", "benchmark mode: read or write")
		dsn         = flag.String("dsn", envOrDefault("DBBENCH_DSN", "root:root123456@tcp(127.0.0.1:3307)/ecommerce_db?charset=utf8mb4&parseTime=true&loc=Local"), "mysql dsn")
		workers     = flag.Int("workers", 64, "concurrent workers")
		duration    = flag.Duration("duration", 30*time.Second, "benchmark duration")
		maxUserID   = flag.Int64("max-user-id", 10000, "max user id for read benchmark")
		maxOrderID  = flag.Int64("max-order-id", 80000, "max order id for read benchmark")
		activityID  = flag.Int64("activity-id", 900001, "activity id for write benchmark")
		productID   = flag.Int64("product-id", 900001, "product id for write benchmark")
		startUserID = flag.Int64("start-user-id", 2000000, "start user id for write benchmark")
		summaryFile = flag.String("summary-file", "", "optional path to write json summary")
	)
	flag.Parse()

	db, err := sql.Open("mysql", *dsn)
	must(err)
	defer db.Close()

	db.SetMaxOpenConns(max(*workers*2, 64))
	db.SetMaxIdleConns(max(*workers, 32))
	db.SetConnMaxLifetime(time.Hour)
	must(db.Ping())

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	startedAt := time.Now()
	resultCh := make(chan workerResult, *workers)
	var okCount int64
	var errCount int64
	var globalSeq int64
	var wg sync.WaitGroup

	for workerID := 0; workerID < *workers; workerID++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(id)*7919))
			localLatencies := make([]int64, 0, 4096)
			localErrors := make([]string, 0, 4)

			for {
				select {
				case <-ctx.Done():
					resultCh <- workerResult{latencies: localLatencies, errors: localErrors}
					return
				default:
				}

				begin := time.Now()
				var runErr error
				switch *mode {
				case "read":
					runErr = runRead(ctx, db, rng, *maxUserID, *maxOrderID)
				case "write":
					seq := atomic.AddInt64(&globalSeq, 1)
					runErr = runWrite(ctx, db, *activityID, *productID, *startUserID+seq)
				default:
					runErr = fmt.Errorf("unsupported mode: %s", *mode)
				}

				localLatencies = append(localLatencies, time.Since(begin).Microseconds())
				if runErr != nil {
					atomic.AddInt64(&errCount, 1)
					if len(localErrors) < 4 {
						localErrors = append(localErrors, runErr.Error())
					}
					continue
				}
				atomic.AddInt64(&okCount, 1)
			}
		}(workerID)
	}

	wg.Wait()
	close(resultCh)

	allLatencies := make([]int64, 0, 16384)
	errorPreview := make([]string, 0, 12)
	for item := range resultCh {
		allLatencies = append(allLatencies, item.latencies...)
		for _, msg := range item.errors {
			if len(errorPreview) >= 12 {
				break
			}
			errorPreview = append(errorPreview, msg)
		}
	}

	finishedAt := time.Now()
	elapsed := finishedAt.Sub(startedAt).Seconds()
	stats := buildSummary(*mode, *workers, startedAt, finishedAt, elapsed, okCount, errCount, allLatencies, errorPreview)
	printSummary(stats)
	if *summaryFile != "" {
		writeSummary(*summaryFile, stats)
	}
}

func runRead(ctx context.Context, db *sql.DB, rng *rand.Rand, maxUserID, maxOrderID int64) error {
	if rng.Intn(10) < 6 {
		id := rng.Int63n(maxOrderID) + 1
		row := db.QueryRowContext(ctx, "SELECT id, order_no, user_id, status, total_amount FROM orders WHERE id=?", id)
		var orderID, userID, totalAmount int64
		var orderNo string
		var status int8
		return row.Scan(&orderID, &orderNo, &userID, &status, &totalAmount)
	}

	userID := rng.Int63n(maxUserID) + 1
	offset := rng.Intn(200)
	rows, err := db.QueryContext(ctx, "SELECT order_no, status, created_at FROM orders WHERE user_id=? ORDER BY id DESC LIMIT 10 OFFSET ?", userID, offset)
	if err != nil {
		return err
	}
	defer rows.Close()

	var orderNo string
	var status int8
	var createdAt time.Time
	for rows.Next() {
		if err := rows.Scan(&orderNo, &status, &createdAt); err != nil {
			return err
		}
	}
	return rows.Err()
}

func runWrite(ctx context.Context, db *sql.DB, activityID, productID, userID int64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	orderNo := fmt.Sprintf("dbbench_%d_%d", userID, time.Now().UnixNano())
	total := int64(199900)

	res, err := tx.ExecContext(
		ctx,
		"INSERT INTO orders (order_no, user_id, activity_id, total_amount, pay_amount, status, order_type, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 0, 1, NOW(), NOW())",
		orderNo, userID, activityID, total, total,
	)
	if err != nil {
		return err
	}

	orderID, err := res.LastInsertId()
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(
		ctx,
		"INSERT INTO order_items (order_id, product_id, snapshot_title, snapshot_price, quantity, total_price, created_at, updated_at) VALUES (?, ?, ?, ?, 1, ?, NOW(), NOW())",
		orderID, productID, "dbbench_item", total, total,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

func buildSummary(mode string, workers int, startedAt, finishedAt time.Time, elapsed float64, okCount, errCount int64, latencies []int64, errorPreview []string) summary {
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	total := okCount + errCount
	throughput := 0.0
	errRate := 0.0
	if elapsed > 0 {
		throughput = float64(okCount) / elapsed
	}
	if total > 0 {
		errRate = float64(errCount) / float64(total)
	}

	return summary{
		Mode:          mode,
		DurationSec:   elapsed,
		Workers:       workers,
		Success:       okCount,
		Failed:        errCount,
		Throughput:    throughput,
		ErrorRate:     errRate,
		P50Ms:         percentileMs(latencies, 0.50),
		P95Ms:         percentileMs(latencies, 0.95),
		P99Ms:         percentileMs(latencies, 0.99),
		MinMs:         percentileMs(latencies, 0.00),
		MaxMs:         percentileMs(latencies, 1.00),
		Samples:       len(latencies),
		StartedAt:     startedAt.Format(time.RFC3339),
		FinishedAt:    finishedAt.Format(time.RFC3339),
		ErrorsPreview: errorPreview,
	}
}

func percentileMs(values []int64, ratio float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if ratio <= 0 {
		return float64(values[0]) / 1000.0
	}
	if ratio >= 1 {
		return float64(values[len(values)-1]) / 1000.0
	}
	index := int(float64(len(values)-1) * ratio)
	return float64(values[index]) / 1000.0
}

func printSummary(s summary) {
	unit := "QPS"
	if s.Mode == "write" {
		unit = "TPS"
	}

	fmt.Println("========== DB BENCHMARK ==========")
	fmt.Printf("mode:          %s\n", s.Mode)
	fmt.Printf("workers:       %d\n", s.Workers)
	fmt.Printf("duration(s):   %.2f\n", s.DurationSec)
	fmt.Printf("%s:           %.2f\n", unit, s.Throughput)
	fmt.Printf("success:       %d\n", s.Success)
	fmt.Printf("failed:        %d\n", s.Failed)
	fmt.Printf("error_rate:    %.4f\n", s.ErrorRate)
	fmt.Printf("p50(ms):       %.2f\n", s.P50Ms)
	fmt.Printf("p95(ms):       %.2f\n", s.P95Ms)
	fmt.Printf("p99(ms):       %.2f\n", s.P99Ms)
	fmt.Printf("min(ms):       %.2f\n", s.MinMs)
	fmt.Printf("max(ms):       %.2f\n", s.MaxMs)
	fmt.Printf("samples:       %d\n", s.Samples)
	if len(s.ErrorsPreview) > 0 {
		fmt.Printf("errors:        %v\n", s.ErrorsPreview)
	}
	fmt.Println("==================================")
}

func writeSummary(path string, s summary) {
	file, err := os.Create(path)
	must(err)
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	must(encoder.Encode(s))
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

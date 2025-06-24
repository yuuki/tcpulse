package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rcrowley/go-metrics"
)

func TestNewPrinter(t *testing.T) {
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	if printer == nil {
		t.Fatal("Expected printer to be created, got nil")
	}
	
	if printer.writer != &buf {
		t.Error("Expected printer writer to be set correctly")
	}
}

func TestPrinter_PrintStatHeader(t *testing.T) {
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	printer.PrintStatHeader()
	
	output := buf.String()
	expectedHeaders := []string{
		"PEER", "CNT", "LAT_MAX(µs)", "LAT_MIN(µs)", "LAT_MEAN(µs)",
		"LAT_90p(µs)", "LAT_95p(µs)", "LAT_99p(µs)", "RATE(/s)",
	}
	
	for _, header := range expectedHeaders {
		if !strings.Contains(output, header) {
			t.Errorf("Expected header to contain %q, got %q", header, output)
		}
	}
	
	// Check that it ends with a newline
	if !strings.HasSuffix(output, "\n") {
		t.Error("Expected header to end with newline")
	}
}

func TestPrinter_PrintStatLine(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		setupTimer func() metrics.Timer
		expectAddr bool
		expectCount int64
	}{
		{
			name: "timer with data",
			addr: "localhost:8080",
			setupTimer: func() metrics.Timer {
				timer := metrics.NewTimer()
				timer.Update(100 * time.Microsecond)
				timer.Update(200 * time.Microsecond)
				timer.Update(300 * time.Microsecond)
				return timer
			},
			expectAddr: true,
			expectCount: 3,
		},
		{
			name: "empty timer",
			addr: "test.server:9090",
			setupTimer: func() metrics.Timer {
				return metrics.NewTimer()
			},
			expectAddr: true,
			expectCount: 0,
		},
		{
			name: "single measurement",
			addr: "single.host:1234",
			setupTimer: func() metrics.Timer {
				timer := metrics.NewTimer()
				timer.Update(500 * time.Microsecond)
				return timer
			},
			expectAddr: true,
			expectCount: 1,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printer := NewPrinter(&buf)
			timer := tt.setupTimer()
			
			printer.PrintStatLine(tt.addr, timer)
			
			output := buf.String()
			
			if tt.expectAddr && !strings.Contains(output, tt.addr) {
				t.Errorf("Expected output to contain address %q, got %q", tt.addr, output)
			}
			
			if tt.expectCount > 0 && !strings.Contains(output, string(rune('0'+tt.expectCount))) {
				t.Errorf("Expected output to contain count %d, got %q", tt.expectCount, output)
			}
			
			// Check that it ends with a newline
			if !strings.HasSuffix(output, "\n") {
				t.Error("Expected stat line to end with newline")
			}
		})
	}
}

func TestPrinter_PrintReport_MergedResults(t *testing.T) {
	// Clear metrics registry
	metrics.DefaultRegistry = metrics.NewRegistry()
	defer func() {
		metrics.DefaultRegistry = metrics.NewRegistry()
	}()
	
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	addrs := []string{"host1:8080", "host2:8081"}
	
	// Set up a merged timer
	timer := metrics.NewTimer()
	metrics.Register("total.latency", timer)
	timer.Update(100 * time.Microsecond)
	timer.Update(200 * time.Microsecond)
	
	printer.PrintReport(addrs, true) // mergeResultsEachHost = true
	
	output := buf.String()
	
	// Check for report header
	if !strings.Contains(output, "--- A result during total execution time ---") {
		t.Errorf("Expected report header, got %q", output)
	}
	
	// Check for merged results indicator
	if !strings.Contains(output, "merged(2 hosts)") {
		t.Errorf("Expected merged results indicator, got %q", output)
	}
	
	// Should not contain individual host addresses
	for _, addr := range addrs {
		if strings.Contains(output, addr) {
			t.Errorf("Expected merged results not to contain individual address %q, got %q", addr, output)
		}
	}
}

func TestPrinter_PrintReport_SeparateResults(t *testing.T) {
	// Clear metrics registry
	metrics.DefaultRegistry = metrics.NewRegistry()
	defer func() {
		metrics.DefaultRegistry = metrics.NewRegistry()
	}()
	
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	addrs := []string{"host1:8080", "host2:8081"}
	
	// Set up individual timers for each host
	for _, addr := range addrs {
		timer := metrics.NewTimer()
		metrics.Register("total.latency."+addr, timer)
		timer.Update(100 * time.Microsecond)
	}
	
	printer.PrintReport(addrs, false) // mergeResultsEachHost = false
	
	output := buf.String()
	
	// Check for report header
	if !strings.Contains(output, "--- A result during total execution time ---") {
		t.Errorf("Expected report header, got %q", output)
	}
	
	// Should contain individual host addresses
	for _, addr := range addrs {
		if !strings.Contains(output, addr) {
			t.Errorf("Expected output to contain address %q, got %q", addr, output)
		}
	}
	
	// Should not contain merged indicator
	if strings.Contains(output, "merged(") {
		t.Errorf("Expected separate results not to contain merged indicator, got %q", output)
	}
}

func TestPrinter_PrintJSONLinesReport_SingleHost(t *testing.T) {
	// Clear metrics registry
	metrics.DefaultRegistry = metrics.NewRegistry()
	defer func() {
		metrics.DefaultRegistry = metrics.NewRegistry()
	}()
	
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	addr := "localhost:8080"
	addrs := []string{addr}
	
	// Set up timer
	timer := metrics.NewTimer()
	metrics.Register("total.latency."+addr, timer)
	timer.Update(100 * time.Microsecond)
	timer.Update(200 * time.Microsecond)
	timer.Update(300 * time.Microsecond)
	
	printer.PrintJSONLinesReport(addrs, false)
	
	output := strings.TrimSpace(buf.String())
	if output == "" {
		t.Fatal("Expected JSON output, got empty string")
	}
	
	var result JSONLinesResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}
	
	if result.Peer != addr {
		t.Errorf("Expected peer %s, got %s", addr, result.Peer)
	}
	
	if result.Count != 3 {
		t.Errorf("Expected count 3, got %d", result.Count)
	}
	
	if result.LatencyMax != 300 {
		t.Errorf("Expected max latency 300, got %d", result.LatencyMax)
	}
	
	if result.LatencyMin != 100 {
		t.Errorf("Expected min latency 100, got %d", result.LatencyMin)
	}
	
	if result.Timestamp == "" {
		t.Error("Expected timestamp to be set")
	}
	
	// Validate timestamp format
	if _, err := time.Parse(time.RFC3339, result.Timestamp); err != nil {
		t.Errorf("Expected valid RFC3339 timestamp, got %s", result.Timestamp)
	}
}

func TestPrinter_PrintJSONLinesReport_MultipleHosts(t *testing.T) {
	// Clear metrics registry
	metrics.DefaultRegistry = metrics.NewRegistry()
	defer func() {
		metrics.DefaultRegistry = metrics.NewRegistry()
	}()
	
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	addrs := []string{"host1:8080", "host2:8081", "host3:8082"}
	
	// Set up timers for each host
	for i, addr := range addrs {
		timer := metrics.NewTimer()
		metrics.Register("total.latency."+addr, timer)
		
		// Add different numbers of measurements for each host
		for j := 0; j < (i+1)*2; j++ {
			timer.Update(time.Duration(100*(j+1)) * time.Microsecond)
		}
	}
	
	printer.PrintJSONLinesReport(addrs, false)
	
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(addrs) {
		t.Fatalf("Expected %d JSON lines, got %d", len(addrs), len(lines))
	}
	
	for i, line := range lines {
		var result JSONLinesResult
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			t.Fatalf("Failed to unmarshal JSON line %d: %v", i, err)
		}
		
		if result.Peer != addrs[i] {
			t.Errorf("Line %d: Expected peer %s, got %s", i, addrs[i], result.Peer)
		}
		
		expectedCount := int64((i + 1) * 2)
		if result.Count != expectedCount {
			t.Errorf("Line %d: Expected count %d, got %d", i, expectedCount, result.Count)
		}
		
		if result.Timestamp == "" {
			t.Errorf("Line %d: Expected timestamp to be set", i)
		}
	}
}

func TestPrinter_PrintJSONLinesReport_MergedHosts(t *testing.T) {
	// Clear metrics registry
	metrics.DefaultRegistry = metrics.NewRegistry()
	defer func() {
		metrics.DefaultRegistry = metrics.NewRegistry()
	}()
	
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	addrs := []string{"host1:8080", "host2:8081"}
	
	// Set up merged timer
	timer := metrics.NewTimer()
	metrics.Register("total.latency", timer)
	timer.Update(100 * time.Microsecond)
	timer.Update(200 * time.Microsecond)
	timer.Update(300 * time.Microsecond)
	timer.Update(400 * time.Microsecond)
	
	printer.PrintJSONLinesReport(addrs, true) // mergeResultsEachHost = true
	
	output := strings.TrimSpace(buf.String())
	lines := strings.Split(output, "\n")
	if len(lines) != 1 {
		t.Fatalf("Expected 1 JSON line for merged hosts, got %d", len(lines))
	}
	
	var result JSONLinesResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}
	
	expectedPeer := "merged(2 hosts)"
	if result.Peer != expectedPeer {
		t.Errorf("Expected peer %s, got %s", expectedPeer, result.Peer)
	}
	
	if result.Count != 4 {
		t.Errorf("Expected count 4, got %d", result.Count)
	}
	
	if result.LatencyMax != 400 {
		t.Errorf("Expected max latency 400, got %d", result.LatencyMax)
	}
	
	if result.LatencyMin != 100 {
		t.Errorf("Expected min latency 100, got %d", result.LatencyMin)
	}
}

func TestPrinter_PrintJSONLinesReport_EmptyTimer(t *testing.T) {
	// Clear metrics registry
	metrics.DefaultRegistry = metrics.NewRegistry()
	defer func() {
		metrics.DefaultRegistry = metrics.NewRegistry()
	}()
	
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	addr := "empty.host:8080"
	addrs := []string{addr}
	
	// Set up empty timer
	timer := metrics.NewTimer()
	metrics.Register("total.latency."+addr, timer)
	
	printer.PrintJSONLinesReport(addrs, false)
	
	output := strings.TrimSpace(buf.String())
	if output == "" {
		t.Fatal("Expected JSON output, got empty string")
	}
	
	var result JSONLinesResult
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}
	
	if result.Peer != addr {
		t.Errorf("Expected peer %s, got %s", addr, result.Peer)
	}
	
	if result.Count != 0 {
		t.Errorf("Expected count 0, got %d", result.Count)
	}
	
	// Empty timer should have zero/default values
	if result.LatencyMax != 0 {
		t.Errorf("Expected max latency 0, got %d", result.LatencyMax)
	}
	
	if result.LatencyMin != 0 {
		t.Errorf("Expected min latency 0, got %d", result.LatencyMin)
	}
}

// Test that printer works with different io.Writer implementations
func TestPrinter_DifferentWriters(t *testing.T) {
	tests := []struct {
		name   string
		writer io.Writer
	}{
		{
			name:   "bytes.Buffer",
			writer: &bytes.Buffer{},
		},
		{
			name:   "strings.Builder",
			writer: &strings.Builder{},
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			printer := NewPrinter(tt.writer)
			
			// Test that PrintStatHeader works
			printer.PrintStatHeader()
			
			// For bytes.Buffer, we can check the content
			if buf, ok := tt.writer.(*bytes.Buffer); ok {
				output := buf.String()
				if !strings.Contains(output, "PEER") {
					t.Errorf("Expected output to contain PEER header")
				}
			}
			
			// For strings.Builder, we can check the content
			if builder, ok := tt.writer.(*strings.Builder); ok {
				output := builder.String()
				if !strings.Contains(output, "PEER") {
					t.Errorf("Expected output to contain PEER header")
				}
			}
		})
	}
}

// Test error handling with a failing writer
type failingWriter struct {
	shouldFail bool
}

func (f *failingWriter) Write(p []byte) (n int, err error) {
	if f.shouldFail {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func TestPrinter_ErrorHandling(t *testing.T) {
	// Clear metrics registry
	metrics.DefaultRegistry = metrics.NewRegistry()
	defer func() {
		metrics.DefaultRegistry = metrics.NewRegistry()
	}()
	
	fw := &failingWriter{shouldFail: true}
	printer := NewPrinter(fw)
	
	addr := "test.host:8080"
	addrs := []string{addr}
	
	// Set up timer
	timer := metrics.NewTimer()
	metrics.Register("total.latency."+addr, timer)
	timer.Update(100 * time.Microsecond)
	
	// This should not panic even with a failing writer
	// The JSON encoder will handle the error internally
	printer.PrintJSONLinesReport(addrs, false)
	
	// Test with working writer to ensure normal operation still works
	fw.shouldFail = false
	var buf bytes.Buffer
	printer2 := NewPrinter(&buf)
	printer2.PrintJSONLinesReport(addrs, false)
	
	if buf.Len() == 0 {
		t.Error("Expected output with working writer")
	}
}

func TestJSONLinesResult_AllFields(t *testing.T) {
	// Clear metrics registry
	metrics.DefaultRegistry = metrics.NewRegistry()
	defer func() {
		metrics.DefaultRegistry = metrics.NewRegistry()
	}()
	
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	addr := "localhost:8080"
	addrs := []string{addr}
	
	// Set up timer with varied data for percentile calculations
	timer := metrics.NewTimer()
	metrics.Register("total.latency."+addr, timer)
	
	testDurations := []time.Duration{
		50 * time.Microsecond,
		100 * time.Microsecond,
		150 * time.Microsecond,
		200 * time.Microsecond,
		250 * time.Microsecond,
		300 * time.Microsecond,
		350 * time.Microsecond,
		400 * time.Microsecond,
		450 * time.Microsecond,
		500 * time.Microsecond,
	}
	
	for _, d := range testDurations {
		timer.Update(d)
	}
	
	printer.PrintJSONLinesReport(addrs, false)
	
	var result JSONLinesResult
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}
	
	// Verify all fields are populated with expected values
	if result.Peer != addr {
		t.Errorf("Expected peer %s, got %s", addr, result.Peer)
	}
	
	if result.Count != int64(len(testDurations)) {
		t.Errorf("Expected count %d, got %d", len(testDurations), result.Count)
	}
	
	if result.LatencyMax <= 0 {
		t.Errorf("Expected positive max latency, got %d", result.LatencyMax)
	}
	
	if result.LatencyMin <= 0 {
		t.Errorf("Expected positive min latency, got %d", result.LatencyMin)
	}
	
	if result.LatencyMean <= 0 {
		t.Errorf("Expected positive mean latency, got %d", result.LatencyMean)
	}
	
	if result.Latency90p <= 0 {
		t.Errorf("Expected positive 90p latency, got %d", result.Latency90p)
	}
	
	if result.Latency95p <= 0 {
		t.Errorf("Expected positive 95p latency, got %d", result.Latency95p)
	}
	
	if result.Latency99p <= 0 {
		t.Errorf("Expected positive 99p latency, got %d", result.Latency99p)
	}
	
	if result.Rate < 0 {
		t.Errorf("Expected non-negative rate, got %f", result.Rate)
	}
	
	if result.Timestamp == "" {
		t.Error("Expected timestamp to be set")
	}
	
	// Validate timestamp format
	if _, err := time.Parse(time.RFC3339, result.Timestamp); err != nil {
		t.Errorf("Expected valid RFC3339 timestamp, got %s", result.Timestamp)
	}
	
	// Verify JSON field names
	var jsonMap map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &jsonMap); err != nil {
		t.Fatalf("Failed to unmarshal JSON to map: %v", err)
	}
	
	expectedFields := []string{
		"peer", "count", "latency_max_us", "latency_min_us", 
		"latency_mean_us", "latency_90p_us", "latency_95p_us", 
		"latency_99p_us", "rate_per_sec", "timestamp",
	}
	
	for _, field := range expectedFields {
		if _, exists := jsonMap[field]; !exists {
			t.Errorf("Expected field %s to exist in JSON output", field)
		}
	}
}

func TestPrinter_StatLineFormatting(t *testing.T) {
	var buf bytes.Buffer
	printer := NewPrinter(&buf)
	
	timer := metrics.NewTimer()
	timer.Update(123456 * time.Nanosecond) // 123.456 microseconds
	
	printer.PrintStatLine("test.host:8080", timer)
	
	output := buf.String()
	
	// Check that the output contains expected formatting
	if !strings.Contains(output, "test.host:8080") {
		t.Errorf("Expected output to contain host address")
	}
	
	if !strings.Contains(output, "1") { // Count should be 1
		t.Errorf("Expected output to contain count")
	}
	
	// Check that values are in microseconds (should contain "123")
	if !strings.Contains(output, "123") {
		t.Errorf("Expected output to contain latency value in microseconds")
	}
}
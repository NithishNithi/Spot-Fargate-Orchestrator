package checker

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNewHealthChecker(t *testing.T) {
	retries := 3
	interval := 2 * time.Second

	hc := NewHealthChecker(retries, interval)

	if hc.retries != retries {
		t.Errorf("Expected retries %d, got %d", retries, hc.retries)
	}

	if hc.interval != interval {
		t.Errorf("Expected interval %v, got %v", interval, hc.interval)
	}

	if hc.httpClient == nil {
		t.Error("Expected httpClient to be initialized")
	}

	if hc.logger == nil {
		t.Error("Expected logger to be initialized")
	}
}

func TestSingleHealthCheck_Success(t *testing.T) {
	// Create a test server that returns 200 OK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))
	defer server.Close()

	hc := NewHealthChecker(1, time.Second)
	result, err := hc.singleHealthCheck(server.URL)

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if !result.Healthy {
		t.Error("Expected result to be healthy")
	}

	if result.StatusCode != 200 {
		t.Errorf("Expected status code 200, got %d", result.StatusCode)
	}

	if result.ResponseTime <= 0 {
		t.Error("Expected positive response time")
	}
}

func TestSingleHealthCheck_Failure(t *testing.T) {
	// Create a test server that returns 500 Internal Server Error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer server.Close()

	hc := NewHealthChecker(1, time.Second)
	result, err := hc.singleHealthCheck(server.URL)

	if err == nil {
		t.Error("Expected error for unhealthy status code")
	}

	if result.Healthy {
		t.Error("Expected result to be unhealthy")
	}

	if result.StatusCode != 500 {
		t.Errorf("Expected status code 500, got %d", result.StatusCode)
	}
}

func TestCheckPodHealth(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("OK"))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Extract host and port from test server URL
	// For this test, we'll use a mock approach since we can't easily extract
	// the exact IP and port in the expected format
	hc := NewHealthChecker(1, time.Second)

	// Test with a URL that will fail (since we can't easily mock the exact pod IP format)
	result, err := hc.CheckPodHealth("127.0.0.1", 8080, "/health")

	// We expect this to fail since there's no server on 127.0.0.1:8080
	if err == nil {
		t.Error("Expected error when connecting to non-existent server")
	}

	if result.Healthy {
		t.Error("Expected result to be unhealthy for non-existent server")
	}
}

func TestHealthCheckStatusCodes(t *testing.T) {
	testCases := []struct {
		statusCode    int
		expectHealthy bool
	}{
		{200, true},
		{201, true},
		{299, true},
		{300, false},
		{404, false},
		{500, false},
	}

	for _, tc := range testCases {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.statusCode)
		}))

		hc := NewHealthChecker(1, time.Second)
		result, _ := hc.singleHealthCheck(server.URL)

		if result.Healthy != tc.expectHealthy {
			t.Errorf("Status code %d: expected healthy=%v, got healthy=%v",
				tc.statusCode, tc.expectHealthy, result.Healthy)
		}

		if result.StatusCode != tc.statusCode {
			t.Errorf("Expected status code %d, got %d", tc.statusCode, result.StatusCode)
		}

		server.Close()
	}
}

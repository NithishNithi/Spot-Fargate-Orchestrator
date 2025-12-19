package metadata

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// MetadataClient handles communication with EC2 metadata service
type MetadataClient struct {
	endpoint    string
	httpClient  *http.Client
	token       string
	tokenExpiry time.Time
}

// InterruptionNotice represents a spot interruption notice from AWS
type InterruptionNotice struct {
	Action string    `json:"action"`
	Time   time.Time `json:"time"`
}

// MetadataClientInterface defines the interface for metadata clients
type MetadataClientInterface interface {
	GetSpotInterruptionNotice() (*InterruptionNotice, error)
	GetInstanceID() (string, error)
	IsSpotInstance() (bool, error)
}

// NewMetadataClient creates a new metadata client
func NewMetadataClient(endpoint string, timeout time.Duration) *MetadataClient {
	return &MetadataClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// NewMetadataClientForEnvironment creates appropriate client based on environment
func NewMetadataClientForEnvironment(endpoint string, timeout time.Duration) MetadataClientInterface {
	// Use mock client for local development
	if os.Getenv("USE_MOCK_METADATA") == "true" {
		return NewMockMetadataClient()
	}

	// Use real client for production
	return NewMetadataClient(endpoint, timeout)
}

// getToken retrieves IMDSv2 token
func (m *MetadataClient) getToken() error {
	// Check if token is still valid (with 30 second buffer)
	if time.Now().Before(m.tokenExpiry.Add(-30*time.Second)) && m.token != "" {
		return nil
	}

	tokenURL := fmt.Sprintf("%s/latest/api/token", m.endpoint)
	req, err := http.NewRequest("PUT", tokenURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create token request: %w", err)
	}

	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "21600") // 6 hours

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get metadata token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to get metadata token, status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read token response: %w", err)
	}

	m.token = strings.TrimSpace(string(body))
	m.tokenExpiry = time.Now().Add(6 * time.Hour)

	return nil
}

// makeAuthenticatedRequest makes a request with IMDSv2 token
func (m *MetadataClient) makeAuthenticatedRequest(url string) (*http.Response, error) {
	if err := m.getToken(); err != nil {
		return nil, fmt.Errorf("failed to get authentication token: %w", err)
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-aws-ec2-metadata-token", m.token)

	return m.httpClient.Do(req)
}

// GetSpotInterruptionNotice retrieves spot interruption notice from metadata service
// Returns nil if no interruption notice exists (404 response)
func (m *MetadataClient) GetSpotInterruptionNotice() (*InterruptionNotice, error) {
	url := fmt.Sprintf("%s/latest/meta-data/spot/instance-action", m.endpoint)

	resp, err := m.makeAuthenticatedRequest(url)
	if err != nil {
		return nil, fmt.Errorf("failed to query spot interruption endpoint: %w", err)
	}
	defer resp.Body.Close()

	// Handle 404 gracefully - no interruption notice exists
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code from metadata service: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var notice InterruptionNotice
	if err := json.Unmarshal(body, &notice); err != nil {
		return nil, fmt.Errorf("failed to parse interruption notice JSON: %w", err)
	}

	return &notice, nil
}

// GetInstanceID retrieves the current instance ID
func (m *MetadataClient) GetInstanceID() (string, error) {
	url := fmt.Sprintf("%s/latest/meta-data/instance-id", m.endpoint)

	resp, err := m.makeAuthenticatedRequest(url)
	if err != nil {
		return "", fmt.Errorf("failed to query instance ID endpoint: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code from metadata service: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	instanceID := strings.TrimSpace(string(body))
	if instanceID == "" {
		return "", fmt.Errorf("empty instance ID returned from metadata service")
	}

	return instanceID, nil
}

// IsSpotInstance checks if the current instance is a spot instance
func (m *MetadataClient) IsSpotInstance() (bool, error) {
	url := fmt.Sprintf("%s/latest/meta-data/spot/instance-action", m.endpoint)

	resp, err := m.makeAuthenticatedRequest(url)
	if err != nil {
		return false, fmt.Errorf("failed to query spot instance endpoint: %w", err)
	}
	defer resp.Body.Close()

	// If the spot endpoint exists (regardless of content), this is a spot instance
	// 404 means not a spot instance, any other response means it is a spot instance
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	// Any other status code (including 200) indicates this is a spot instance
	return true, nil
}

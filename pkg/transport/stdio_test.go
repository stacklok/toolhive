package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/logger"
)

// MockHTTPProxy is a mock implementation of types.Proxy
type MockHTTPProxy struct {
	mock.Mock
}

func (m *MockHTTPProxy) Start(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockHTTPProxy) Stop(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockHTTPProxy) GetMessageChannel() chan jsonrpc2.Message {
	args := m.Called()
	return args.Get(0).(chan jsonrpc2.Message)
}

func (m *MockHTTPProxy) ForwardResponseToClients(ctx context.Context, msg jsonrpc2.Message) error {
	args := m.Called(ctx, msg)
	return args.Error(0)
}

func (m *MockHTTPProxy) SendMessageToDestination(msg jsonrpc2.Message) error {
	args := m.Called(msg)
	return args.Error(0)
}

func TestSanitizeJSONString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "valid JSON",
			input:    []byte(`{"jsonrpc": "2.0", "method": "test", "params": {}}`),
			expected: `{"jsonrpc": "2.0", "method": "test", "params": {}}`,
		},
		{
			name: "JSON with replacement character",
			input: []byte(
				`[` +
					`{` +
					string([]byte{0xEF, 0xBF, 0xBD}) + // U+FFFD
					`"jsonrpc": "2.0", "method": "test", "params": {"data": "test"}` +
					string([]byte{0xEF, 0xBF, 0xBD}) + // U+FFFD
					`}` +
					`]`),
			expected: `{"jsonrpc": "2.0", "method": "test", "params": {"data": "test"}}`,
		},
		{
			name:     "JSON with control characters",
			input:    []byte("\x01{\"jsonrpc\": \"2.0\", \"method\": \"test\", \"params\": {\"data\": \"test\"}\x01}"),
			expected: `{"jsonrpc": "2.0", "method": "test", "params": {"data": "test"}}`,
		},
		{
			name:     "empty array",
			input:    []byte(`[]`),
			expected: ``,
		},
		{
			name:     "invalid JSON",
			input:    []byte(`not a json`),
			expected: ``,
		},
		{
			name:     "JSON with extra content",
			input:    []byte(`extra{"jsonrpc": "2.0"}extra`),
			expected: `{"jsonrpc": "2.0"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fmt.Println(string(tt.input))
			result := sanitizeJSONString(string(tt.input))
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestParseAndForwardJSONRPC(t *testing.T) {
	t.Parallel()
	// Initialize logger for testing
	logger.Initialize()

	tests := []struct {
		name          string
		input         []byte
		shouldForward bool
	}{
		{
			name:          "valid JSON-RPC",
			input:         []byte(`{"jsonrpc": "2.0", "method": "test", "params": {}}`),
			shouldForward: true,
		},
		{
			name:          "empty array",
			input:         []byte(`[]`),
			shouldForward: false,
		},
		{
			name:          "empty string",
			input:         []byte(``),
			shouldForward: false,
		},
		{
			name: "JSON with replacement character",
			input: []byte(
				`{` +
					`"jsonrpc": "2.0", "method": "test", "params": {"data": "test"}` +
					string([]byte{0xEF, 0xBF, 0xBD}) + // U+FFFD
					`}`),
			shouldForward: true,
		},
		{
			name:          "JSON with control characters",
			input:         []byte("\x01{\"jsonrpc\": \"2.0\", \"method\": \"test\", \"params\": {\"data\": \"test\"}\x01}"),
			shouldForward: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Create mock HTTP proxy
			mockProxy := new(MockHTTPProxy)

			// Create transport with mock proxy
			transport := &StdioTransport{
				httpProxy: mockProxy,
			}

			// Set up expectations if the message should be forwarded
			if tt.shouldForward {
				mockProxy.On("ForwardResponseToClients", mock.Anything, mock.Anything).Return(nil)
			}

			// Call the function
			transport.parseAndForwardJSONRPC(context.Background(), string(tt.input))

			// Verify expectations
			mockProxy.AssertExpectations(t)
		})
	}
}

func TestIsSpace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    rune
		expected bool
	}{
		{
			name:     "space character",
			input:    ' ',
			expected: true,
		},
		{
			name:     "newline character",
			input:    '\n',
			expected: true,
		},
		{
			name:     "tab character",
			input:    '\t',
			expected: true,
		},
		{
			name:     "carriage return",
			input:    '\r',
			expected: true,
		},
		{
			name:     "regular character",
			input:    'a',
			expected: false,
		},
		{
			name:     "number",
			input:    '1',
			expected: false,
		},
		{
			name:     "special character",
			input:    '@',
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := isSpace(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// mockReadCloser is a mock implementation of io.ReadCloser for testing
type mockReadCloser struct {
	data      []byte
	readIndex int
	closed    bool
	eofAfter  int // return EOF after this many reads
	readCount int
}

func newMockReadCloser(data string) *mockReadCloser {
	return &mockReadCloser{
		data:     []byte(data),
		eofAfter: -1, // Never EOF by default
	}
}

func newMockReadCloserWithEOF(data string) *mockReadCloser {
	return &mockReadCloser{
		data:     []byte(data),
		eofAfter: 1, // Always EOF after first read for these tests
	}
}

func (m *mockReadCloser) Read(p []byte) (n int, err error) {
	m.readCount++
	if m.eofAfter >= 0 && m.readCount > m.eofAfter {
		return 0, io.EOF
	}

	if m.closed {
		return 0, errors.New("read from closed reader")
	}

	if m.readIndex >= len(m.data) {
		// If eofAfter is set, return EOF
		if m.eofAfter >= 0 {
			return 0, io.EOF
		}
		// Otherwise, block until closed
		time.Sleep(10 * time.Millisecond)
		return 0, nil
	}

	n = copy(p, m.data[m.readIndex:])
	m.readIndex += n
	return n, nil
}

func (m *mockReadCloser) Close() error {
	m.closed = true
	return nil
}

// mockWriteCloser is a mock implementation of io.WriteCloser for testing
type mockWriteCloser struct {
	buffer bytes.Buffer
	closed bool
}

func newMockWriteCloser() *mockWriteCloser {
	return &mockWriteCloser{}
}

func (m *mockWriteCloser) Write(p []byte) (n int, err error) {
	if m.closed {
		return 0, errors.New("write to closed writer")
	}
	return m.buffer.Write(p)
}

func (m *mockWriteCloser) Close() error {
	m.closed = true
	return nil
}

// testRetryConfig returns a fast retry configuration for testing
func testRetryConfig() *retryConfig {
	return &retryConfig{
		maxRetries:   3,
		initialDelay: 10 * time.Millisecond,
		maxDelay:     50 * time.Millisecond,
	}
}

func TestProcessStdout_EOFWithSuccessfulReattachment(t *testing.T) {
	t.Parallel()

	// Initialize logger
	logger.Initialize()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Create mock deployer
	mockDeployer := mocks.NewMockRuntime(ctrl)

	// Create mock stdout that will return EOF after first read
	mockStdout := newMockReadCloserWithEOF(`{"jsonrpc": "2.0", "method": "test", "params": {}}`)

	// Create new stdio streams for re-attachment
	newStdin := newMockWriteCloser()
	newStdout := newMockReadCloser(`{"jsonrpc": "2.0", "method": "test2", "params": {}}`)

	// Set up expectations
	mockDeployer.EXPECT().
		IsWorkloadRunning(gomock.Any(), "test-container").
		Return(true, nil).
		Times(1)

	mockDeployer.EXPECT().
		AttachToWorkload(gomock.Any(), "test-container").
		Return(newStdin, newStdout, nil).
		Times(1)

	// Create mock HTTP proxy
	mockProxy := new(MockHTTPProxy)
	mockProxy.On("ForwardResponseToClients", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Create transport with fast retry config for testing
	transport := &StdioTransport{
		containerName: "test-container",
		deployer:      mockDeployer,
		httpProxy:     mockProxy,
		stdin:         newMockWriteCloser(),
		shutdownCh:    make(chan struct{}),
		retryConfig:   testRetryConfig(),
	}

	// Run processStdout in a goroutine
	done := make(chan struct{})
	go func() {
		transport.processStdout(ctx, mockStdout)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Success - processStdout returned
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for processStdout to complete")
	}

	// Verify that stdin and stdout were updated
	transport.mutex.Lock()
	assert.Equal(t, newStdin, transport.stdin)
	assert.Equal(t, newStdout, transport.stdout)
	transport.mutex.Unlock()
}

func TestProcessStdout_EOFWithDockerUnavailable(t *testing.T) {
	t.Parallel()

	// Initialize logger
	logger.Initialize()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Create mock deployer
	mockDeployer := mocks.NewMockRuntime(ctrl)

	// Create mock stdout that will return EOF
	mockStdout := newMockReadCloserWithEOF(`{"jsonrpc": "2.0", "method": "test", "params": {}}`)

	// Simulate Docker being unavailable on first check, then available
	callCount := 0
	mockDeployer.EXPECT().
		IsWorkloadRunning(gomock.Any(), "test-container").
		DoAndReturn(func(_ context.Context, _ string) (bool, error) {
			callCount++
			if callCount == 1 {
				// First call: Docker socket unavailable
				return false, errors.New("EOF")
			}
			// Second call: Docker is back, container is running
			return true, nil
		}).
		MinTimes(2)

	// Create new stdio streams for re-attachment
	newStdin := newMockWriteCloser()
	newStdout := newMockReadCloser(`{"jsonrpc": "2.0", "method": "test2", "params": {}}`)

	mockDeployer.EXPECT().
		AttachToWorkload(gomock.Any(), "test-container").
		Return(newStdin, newStdout, nil).
		Times(1)

	// Create mock HTTP proxy
	mockProxy := new(MockHTTPProxy)
	mockProxy.On("ForwardResponseToClients", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Create transport with fast retry config for testing
	transport := &StdioTransport{
		containerName: "test-container",
		deployer:      mockDeployer,
		httpProxy:     mockProxy,
		stdin:         newMockWriteCloser(),
		shutdownCh:    make(chan struct{}),
		retryConfig:   testRetryConfig(),
	}

	// Run processStdout in a goroutine
	done := make(chan struct{})
	go func() {
		transport.processStdout(ctx, mockStdout)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Success - processStdout returned
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for processStdout to handle Docker restart")
	}

	// Verify that stdin and stdout were updated after re-attachment
	transport.mutex.Lock()
	assert.Equal(t, newStdin, transport.stdin)
	assert.Equal(t, newStdout, transport.stdout)
	transport.mutex.Unlock()
}

func TestProcessStdout_EOFWithContainerNotRunning(t *testing.T) {
	t.Parallel()

	// Initialize logger
	logger.Initialize()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Create mock deployer
	mockDeployer := mocks.NewMockRuntime(ctrl)

	// Create mock stdout that will return EOF
	mockStdout := newMockReadCloserWithEOF(`{"jsonrpc": "2.0", "method": "test", "params": {}}`)

	// Set up expectations - container is not running
	mockDeployer.EXPECT().
		IsWorkloadRunning(gomock.Any(), "test-container").
		Return(false, nil).
		Times(1)

	// Create mock HTTP proxy
	mockProxy := new(MockHTTPProxy)
	mockProxy.On("ForwardResponseToClients", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Create transport with fast retry config for testing
	transport := &StdioTransport{
		containerName: "test-container",
		deployer:      mockDeployer,
		httpProxy:     mockProxy,
		stdin:         newMockWriteCloser(),
		shutdownCh:    make(chan struct{}),
		retryConfig:   testRetryConfig(),
	}

	// Run processStdout in a goroutine
	done := make(chan struct{})
	go func() {
		transport.processStdout(ctx, mockStdout)
		close(done)
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		// Success - processStdout returned
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Test timed out")
	}
}

func TestProcessStdout_EOFWithFailedReattachment(t *testing.T) {
	t.Parallel()

	// Initialize logger
	logger.Initialize()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Use shorter timeout now that we have fast retries
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	// Create mock deployer
	mockDeployer := mocks.NewMockRuntime(ctrl)

	// Create mock stdout that will return EOF
	mockStdout := newMockReadCloserWithEOF(`{"jsonrpc": "2.0", "method": "test", "params": {}}`)

	retryCount := 0
	// Set up expectations - container is running but re-attachment fails
	mockDeployer.EXPECT().
		IsWorkloadRunning(gomock.Any(), "test-container").
		DoAndReturn(func(_ context.Context, _ string) (bool, error) {
			retryCount++
			return true, nil
		}).
		AnyTimes()

	mockDeployer.EXPECT().
		AttachToWorkload(gomock.Any(), "test-container").
		Return(nil, nil, errors.New("failed to attach")).
		AnyTimes()

	// Create mock HTTP proxy
	mockProxy := new(MockHTTPProxy)
	mockProxy.On("ForwardResponseToClients", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Create transport with fast retry config for testing
	transport := &StdioTransport{
		containerName: "test-container",
		deployer:      mockDeployer,
		httpProxy:     mockProxy,
		stdin:         newMockWriteCloser(),
		shutdownCh:    make(chan struct{}),
		retryConfig:   testRetryConfig(),
	}

	// Store original stdin/stdout
	originalStdin := transport.stdin

	// Run processStdout in a goroutine
	done := make(chan struct{})
	go func() {
		transport.processStdout(ctx, mockStdout)
		close(done)
	}()

	// Wait for completion
	select {
	case <-done:
		// Success - processStdout returned
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for context timeout")
	}

	// Verify that we attempted at least one retry
	assert.GreaterOrEqual(t, retryCount, 1, "Expected at least 1 retry attempt")

	// Verify that stdin/stdout were NOT updated since re-attachment failed
	transport.mutex.Lock()
	assert.Equal(t, originalStdin, transport.stdin)
	transport.mutex.Unlock()
}

func TestProcessStdout_EOFWithReattachmentRetryLogic(t *testing.T) {
	t.Parallel()

	// Initialize logger
	logger.Initialize()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Create mock deployer
	mockDeployer := mocks.NewMockRuntime(ctrl)

	// Create mock stdout that will return EOF
	mockStdout := newMockReadCloserWithEOF(`{"jsonrpc": "2.0", "method": "test", "params": {}}`)

	// Track retry attempts
	attemptCount := 0

	// Set up expectations - fail first 2 attempts, succeed on 3rd
	mockDeployer.EXPECT().
		IsWorkloadRunning(gomock.Any(), "test-container").
		DoAndReturn(func(_ context.Context, _ string) (bool, error) {
			attemptCount++
			if attemptCount <= 2 {
				// First 2 attempts: connection refused (Docker restarting)
				return false, errors.New("connection refused")
			}
			// Third attempt: success
			return true, nil
		}).
		MinTimes(3)

	// Create new stdio streams for successful re-attachment
	newStdin := newMockWriteCloser()
	newStdout := newMockReadCloser(`{"jsonrpc": "2.0", "method": "test2", "params": {}}`)

	mockDeployer.EXPECT().
		AttachToWorkload(gomock.Any(), "test-container").
		Return(newStdin, newStdout, nil).
		Times(1)

	// Create mock HTTP proxy
	mockProxy := new(MockHTTPProxy)
	mockProxy.On("ForwardResponseToClients", mock.Anything, mock.Anything).Return(nil).Maybe()

	// Create transport with fast retry config for testing
	transport := &StdioTransport{
		containerName: "test-container",
		deployer:      mockDeployer,
		httpProxy:     mockProxy,
		stdin:         newMockWriteCloser(),
		shutdownCh:    make(chan struct{}),
		retryConfig:   testRetryConfig(),
	}

	// Run processStdout in a goroutine
	done := make(chan struct{})
	go func() {
		transport.processStdout(ctx, mockStdout)
		close(done)
	}()

	// Wait for completion
	select {
	case <-done:
		// Success - processStdout returned after retries
	case <-time.After(1 * time.Second):
		t.Fatal("Test timed out waiting for retry logic to complete")
	}

	// Verify that we had multiple retry attempts
	require.GreaterOrEqual(t, attemptCount, 3, "Expected at least 3 retry attempts")

	// Verify that stdin and stdout were eventually updated
	transport.mutex.Lock()
	assert.Equal(t, newStdin, transport.stdin)
	assert.Equal(t, newStdout, transport.stdout)
	transport.mutex.Unlock()
}

func TestProcessStdout_EOFCheckErrorTypes(t *testing.T) {
	t.Parallel()

	// Initialize logger
	logger.Initialize()

	tests := []struct {
		name           string
		checkError     error
		shouldRetry    bool
		contextTimeout time.Duration
	}{
		{
			name:           "Docker socket EOF error triggers retry",
			checkError:     errors.New("EOF"),
			shouldRetry:    true,
			contextTimeout: 500 * time.Millisecond,
		},
		{
			name:           "Connection refused triggers retry",
			checkError:     errors.New("connection refused"),
			shouldRetry:    true,
			contextTimeout: 500 * time.Millisecond,
		},
		{
			name:           "Other errors still retry",
			checkError:     errors.New("some other error"),
			shouldRetry:    true,
			contextTimeout: 500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			ctx, cancel := context.WithTimeout(context.Background(), tt.contextTimeout)
			defer cancel()

			// Create mock deployer
			mockDeployer := mocks.NewMockRuntime(ctrl)

			// Create mock stdout that will return EOF
			mockStdout := newMockReadCloserWithEOF(`{"jsonrpc": "2.0", "method": "test"}`)

			// Track how many times IsWorkloadRunning is called
			callCount := 0

			// Set up expectations - allow unlimited calls since we're testing retry behavior
			mockDeployer.EXPECT().
				IsWorkloadRunning(gomock.Any(), "test-container").
				DoAndReturn(func(_ context.Context, _ string) (bool, error) {
					callCount++
					return false, tt.checkError
				}).
				AnyTimes()

			// Create mock HTTP proxy
			mockProxy := new(MockHTTPProxy)
			mockProxy.On("ForwardResponseToClients", mock.Anything, mock.Anything).Return(nil).Maybe()

			// Create transport with fast retry config for testing
			transport := &StdioTransport{
				containerName: "test-container",
				deployer:      mockDeployer,
				httpProxy:     mockProxy,
				stdin:         newMockWriteCloser(),
				shutdownCh:    make(chan struct{}),
				retryConfig:   testRetryConfig(),
			}

			// Run processStdout in a goroutine
			done := make(chan struct{})
			go func() {
				transport.processStdout(ctx, mockStdout)
				close(done)
			}()

			// Wait for completion
			select {
			case <-done:
				// Success
			case <-time.After(tt.contextTimeout + 500*time.Millisecond):
				t.Fatal("Test timed out")
			}

			// Verify we got at least one retry attempt
			assert.GreaterOrEqual(t, callCount, 1, "Expected at least 1 retry attempt")
		})
	}
}

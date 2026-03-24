package ollama

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	ollamaapi "github.com/ollama/ollama/api"

	"tops/internal/obs"
)

type Manager interface {
	EnsureRunning(ctx context.Context, endpoint string) error
	ListModels(ctx context.Context, endpoint string) ([]string, error)
	WarmModel(ctx context.Context, endpoint string, model string) error
}

type StartServeFunc func(ctx context.Context) error

type OllamaClient interface {
	Heartbeat(ctx context.Context) error
	List(ctx context.Context) (*ollamaapi.ListResponse, error)
	Generate(ctx context.Context, req *ollamaapi.GenerateRequest, fn ollamaapi.GenerateResponseFunc) error
}

type ClientFactory func(baseURL string, httpClient *http.Client) (OllamaClient, error)

type Options struct {
	Client        *http.Client
	ClientFactory ClientFactory
	StartServe    StartServeFunc
	Logger        *obs.Logger
	MaxAttempts   int
	RetryDelay    time.Duration
}

type manager struct {
	client        *http.Client
	clientFactory ClientFactory
	startServe    StartServeFunc
	logger        *obs.Logger
	maxAttempts   int
	retryDelay    time.Duration
}

func NewManager(opts Options) Manager {
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	clientFactory := opts.ClientFactory
	if clientFactory == nil {
		clientFactory = defaultClientFactory
	}
	startServe := opts.StartServe
	if startServe == nil {
		startServe = defaultStartServe
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 20
	}
	retryDelay := opts.RetryDelay
	if retryDelay <= 0 {
		retryDelay = 500 * time.Millisecond
	}
	return &manager{
		client:        client,
		clientFactory: clientFactory,
		startServe:    startServe,
		logger:        opts.Logger,
		maxAttempts:   maxAttempts,
		retryDelay:    retryDelay,
	}
}

func (m *manager) EnsureRunning(ctx context.Context, endpoint string) error {
	base := NormalizeBaseURL(endpoint)
	if base == "" {
		return errors.New("ollama endpoint is empty")
	}
	if m.isHealthy(ctx, base) {
		if m.logger != nil && m.logger.Enabled() {
			m.logger.Printf("ollama ensure-running endpoint=%s status=already-running", base)
		}
		return nil
	}

	if !IsLocalEndpoint(base) {
		return fmt.Errorf("ollama is unreachable at %s and auto-start is disabled for non-local endpoints", base)
	}

	if m.logger != nil && m.logger.Enabled() {
		m.logger.Printf("ollama ensure-running endpoint=%s status=starting", base)
	}
	if err := m.startServe(ctx); err != nil {
		return fmt.Errorf("failed to start local ollama service: %w. Install Ollama and run `ollama serve`", err)
	}

	for attempt := 1; attempt <= m.maxAttempts; attempt++ {
		if m.isHealthy(ctx, base) {
			if m.logger != nil && m.logger.Enabled() {
				m.logger.Printf("ollama ensure-running endpoint=%s status=started attempts=%d", base, attempt)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for ollama to start cancelled: %w", ctx.Err())
		case <-time.After(m.retryDelay):
		}
	}
	return fmt.Errorf("ollama did not become ready at %s. Run `ollama serve` and retry", base)
}

func (m *manager) ListModels(ctx context.Context, endpoint string) ([]string, error) {
	base := NormalizeBaseURL(endpoint)
	if base == "" {
		return nil, errors.New("ollama endpoint is empty")
	}
	client, err := m.clientFor(base, 2*time.Minute)
	if err != nil {
		return nil, err
	}
	resp, err := client.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("query ollama tags endpoint: %w", err)
	}
	models := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		name := strings.TrimSpace(model.Name)
		if name != "" {
			models = append(models, name)
		}
	}
	return models, nil
}

func (m *manager) WarmModel(ctx context.Context, endpoint string, model string) error {
	base := NormalizeBaseURL(endpoint)
	if base == "" {
		return errors.New("ollama endpoint is empty")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return errors.New("ollama model is empty")
	}
	client, err := m.clientFor(base, 2*time.Minute)
	if err != nil {
		return err
	}
	stream := false
	req := &ollamaapi.GenerateRequest{
		Model:  model,
		Prompt: "ping",
		Stream: &stream,
		Options: map[string]any{
			"num_predict": 1,
		},
	}
	if err := client.Generate(ctx, req, func(ollamaapi.GenerateResponse) error {
		return nil
	}); err != nil {
		var statusErr ollamaapi.StatusError
		if errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusNotFound {
			return fmt.Errorf("model %q is not available locally. Run `ollama pull %s`", model, model)
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "not found") {
			return fmt.Errorf("model %q is not available locally. Run `ollama pull %s`", model, model)
		}
		return fmt.Errorf("request ollama warm-model: %w", err)
	}
	if m.logger != nil && m.logger.Enabled() {
		m.logger.Printf("ollama warm-model endpoint=%s model=%s status=ok", base, model)
	}
	return nil
}

func (m *manager) isHealthy(ctx context.Context, base string) bool {
	client, err := m.clientFor(base, 2*time.Second)
	if err != nil {
		return false
	}
	return client.Heartbeat(ctx) == nil
}

func (m *manager) clientFor(base string, minTimeout time.Duration) (OllamaClient, error) {
	httpClient := m.withMinTimeout(minTimeout)
	return m.clientFactory(base, httpClient)
}

func (m *manager) withMinTimeout(minTimeout time.Duration) *http.Client {
	if m.client == nil {
		return &http.Client{Timeout: minTimeout}
	}
	if m.client.Timeout == 0 || m.client.Timeout < minTimeout {
		copyClient := *m.client
		copyClient.Timeout = minTimeout
		return &copyClient
	}
	return m.client
}

func defaultClientFactory(baseURL string, httpClient *http.Client) (OllamaClient, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse ollama endpoint: %w", err)
	}
	return ollamaapi.NewClient(u, httpClient), nil
}

func NormalizeBaseURL(raw string) string {
	base := strings.TrimSpace(raw)
	base = strings.TrimSuffix(base, "/")
	base = strings.TrimSuffix(base, "/api/chat")
	base = strings.TrimSuffix(base, "/api/tags")
	base = strings.TrimSuffix(base, "/v1/chat/completions")
	base = strings.TrimSuffix(base, "/v1")
	if base == "" {
		return "http://localhost:11434"
	}
	return base
}

func IsLocalEndpoint(endpoint string) bool {
	raw := strings.TrimSpace(endpoint)
	if raw == "" {
		return true
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func defaultStartServe(ctx context.Context) error {
	if _, err := exec.LookPath("ollama"); err != nil {
		return fmt.Errorf("ollama binary not found in PATH")
	}
	cmd := exec.Command("ollama", "serve")
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

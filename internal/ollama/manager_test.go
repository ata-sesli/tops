package ollama

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ollamaapi "github.com/ollama/ollama/api"
)

type fakeClient struct {
	heartbeatFn func(ctx context.Context) error
	listFn      func(ctx context.Context) (*ollamaapi.ListResponse, error)
	generateFn  func(ctx context.Context, req *ollamaapi.GenerateRequest, fn ollamaapi.GenerateResponseFunc) error
}

func (f fakeClient) Heartbeat(ctx context.Context) error {
	if f.heartbeatFn == nil {
		return nil
	}
	return f.heartbeatFn(ctx)
}

func (f fakeClient) List(ctx context.Context) (*ollamaapi.ListResponse, error) {
	if f.listFn == nil {
		return &ollamaapi.ListResponse{}, nil
	}
	return f.listFn(ctx)
}

func (f fakeClient) Generate(ctx context.Context, req *ollamaapi.GenerateRequest, fn ollamaapi.GenerateResponseFunc) error {
	if f.generateFn == nil {
		return nil
	}
	return f.generateFn(ctx, req, fn)
}

func TestEnsureRunningAlreadyRunningDoesNotStart(t *testing.T) {
	var startCalls int32
	mgr := NewManager(Options{
		ClientFactory: func(baseURL string, httpClient *http.Client) (OllamaClient, error) {
			return fakeClient{}, nil
		},
		StartServe: func(ctx context.Context) error {
			atomic.AddInt32(&startCalls, 1)
			return nil
		},
		MaxAttempts: 1,
		RetryDelay:  time.Millisecond,
	})

	if err := mgr.EnsureRunning(context.Background(), "http://localhost:11434"); err != nil {
		t.Fatalf("expected running check to succeed: %v", err)
	}
	if atomic.LoadInt32(&startCalls) != 0 {
		t.Fatalf("expected no start calls, got %d", startCalls)
	}
}

func TestEnsureRunningStartsLocalWhenUnavailable(t *testing.T) {
	var startCalls int32
	var heartbeats int32
	mgr := NewManager(Options{
		ClientFactory: func(baseURL string, httpClient *http.Client) (OllamaClient, error) {
			return fakeClient{
				heartbeatFn: func(ctx context.Context) error {
					if atomic.AddInt32(&heartbeats, 1) == 1 {
						return errors.New("connection refused")
					}
					return nil
				},
			}, nil
		},
		StartServe: func(ctx context.Context) error {
			atomic.AddInt32(&startCalls, 1)
			return nil
		},
		MaxAttempts: 1,
		RetryDelay:  time.Millisecond,
	})

	if err := mgr.EnsureRunning(context.Background(), "http://localhost:11434"); err != nil {
		t.Fatalf("expected local start path to succeed: %v", err)
	}
	if atomic.LoadInt32(&startCalls) != 1 {
		t.Fatalf("expected one start call, got %d", startCalls)
	}
}

func TestEnsureRunningDoesNotStartRemote(t *testing.T) {
	var startCalls int32
	mgr := NewManager(Options{
		ClientFactory: func(baseURL string, httpClient *http.Client) (OllamaClient, error) {
			return fakeClient{
				heartbeatFn: func(ctx context.Context) error {
					return errors.New("connection refused")
				},
			}, nil
		},
		StartServe: func(ctx context.Context) error {
			atomic.AddInt32(&startCalls, 1)
			return nil
		},
		MaxAttempts: 1,
		RetryDelay:  time.Millisecond,
	})

	err := mgr.EnsureRunning(context.Background(), "https://remote.example.com:11434")
	if err == nil {
		t.Fatal("expected remote ensure-running error")
	}
	if atomic.LoadInt32(&startCalls) != 0 {
		t.Fatalf("expected no start call for remote endpoint, got %d", startCalls)
	}
}

func TestEnsureRunningStartFailure(t *testing.T) {
	mgr := NewManager(Options{
		ClientFactory: func(baseURL string, httpClient *http.Client) (OllamaClient, error) {
			return fakeClient{
				heartbeatFn: func(ctx context.Context) error {
					return errors.New("connection refused")
				},
			}, nil
		},
		StartServe: func(ctx context.Context) error {
			return errors.New("binary not found")
		},
		MaxAttempts: 1,
		RetryDelay:  time.Millisecond,
	})

	err := mgr.EnsureRunning(context.Background(), "http://localhost:11434")
	if err == nil {
		t.Fatal("expected start failure error")
	}
	if got := err.Error(); got == "" || !strings.Contains(strings.ToLower(got), "install ollama") {
		t.Fatalf("expected remediation guidance, got: %v", err)
	}
}

func TestListModels(t *testing.T) {
	mgr := NewManager(Options{
		ClientFactory: func(baseURL string, httpClient *http.Client) (OllamaClient, error) {
			return fakeClient{
				listFn: func(ctx context.Context) (*ollamaapi.ListResponse, error) {
					return &ollamaapi.ListResponse{
						Models: []ollamaapi.ListModelResponse{
							{Name: "llama3.1"},
							{Name: "qwen2.5"},
						},
					}, nil
				},
			}, nil
		},
	})

	models, err := mgr.ListModels(context.Background(), "http://localhost:11434")
	if err != nil {
		t.Fatalf("list models failed: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
}

func TestWarmModel(t *testing.T) {
	var requestModel string
	mgr := NewManager(Options{
		ClientFactory: func(baseURL string, httpClient *http.Client) (OllamaClient, error) {
			return fakeClient{
				generateFn: func(ctx context.Context, req *ollamaapi.GenerateRequest, fn ollamaapi.GenerateResponseFunc) error {
					requestModel = req.Model
					if req.Options["num_predict"] != 1 {
						t.Fatalf("expected num_predict=1, got %#v", req.Options["num_predict"])
					}
					return fn(ollamaapi.GenerateResponse{Response: "ok", Done: true})
				},
			}, nil
		},
	})

	if err := mgr.WarmModel(context.Background(), "http://localhost:11434", "llama3.1"); err != nil {
		t.Fatalf("warm model failed: %v", err)
	}
	if requestModel != "llama3.1" {
		t.Fatalf("unexpected model in warm request: %q", requestModel)
	}
}

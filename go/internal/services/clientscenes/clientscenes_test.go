// channel.go + tests for the clientscenes package.

package clientscenes

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// ---------------------------------------------------------------------------
// tests: httptest mocks per the plan (settings family HTTP services are
// tested against httptest, not live endpoints).
// ---------------------------------------------------------------------------

type realHTTPClient struct{ base string }

func (r *realHTTPClient) GetJSON(ctx context.Context, target *url.URL) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return resp.StatusCode, buf, nil
}

func TestListAgainstMockServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ScenesPath {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":[{"namespace":"ns","scene":"s","options":{}}]}`))
	}))
	defer server.Close()
	svc := NewService(Options{
		Client:            &realHTTPClient{},
		RuntimeAPIBaseURL: func() string { return server.URL },
	})
	response, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != 0 || response.Msg != "ok" {
		t.Fatalf("envelope wrong: %+v", response)
	}
	var configs []map[string]any
	if err := json.Unmarshal(response.Data, &configs); err != nil || len(configs) != 1 || configs[0]["namespace"] != "ns" {
		t.Fatalf("data wrong: %s (%v)", response.Data, err)
	}
}

func TestListHTTPErrorUsesBodyMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad token"}`))
	}))
	defer server.Close()
	svc := NewService(Options{
		Client:            &realHTTPClient{},
		RuntimeAPIBaseURL: func() string { return server.URL },
	})
	_, err := svc.List(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want ApiError, got %v", err)
	}
	if apiErr.Message != "bad token" || apiErr.Status != 401 || apiErr.Method != "GET" {
		t.Fatalf("api error wrong: %+v", apiErr)
	}
}

func TestListHTTPErrorFallsBackToBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer server.Close()
	svc := NewService(Options{
		Client:            &realHTTPClient{},
		RuntimeAPIBaseURL: func() string { return server.URL },
	})
	_, err := svc.List(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "upstream down" {
		t.Fatalf("fallback message wrong: %v", err)
	}
}

func TestListInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()
	svc := NewService(Options{
		Client:            &realHTTPClient{},
		RuntimeAPIBaseURL: func() string { return server.URL },
	})
	_, err := svc.List(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Message != "Invalid JSON response" {
		t.Fatalf("invalid json error wrong: %v", err)
	}
}

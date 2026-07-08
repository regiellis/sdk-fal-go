package fal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// submitBody writes a queue submit response whose URLs point back at the
// request's own host so the handle polls the same test server.
func submitBody(w http.ResponseWriter, r *http.Request, requestID string) {
	base := "http://" + r.Host + "/requests/" + requestID
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"request_id":   requestID,
		"status_url":   base + "/status",
		"response_url": base,
		"cancel_url":   base + "/cancel",
	})
}

func TestSubmitWire(t *testing.T) {
	var gotPath, gotWebhook, gotPriority, gotHint string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotWebhook = r.URL.Query().Get("fal_webhook")
		gotPriority = r.Header.Get("X-Fal-Queue-Priority")
		gotHint = r.Header.Get("X-Fal-Runner-Hint")
		submitBody(w, r, "req-1")
	}))
	defer srv.Close()

	c := testClient(srv.URL)
	req, err := c.Submit(context.Background(), "fal-ai/flux", map[string]int{"n": 1},
		WithPath("dev"),
		WithWebhook("https://example.com/hook"),
		WithPriority(PriorityLow),
		WithHint("runner-3"),
	)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if req.ID != "req-1" {
		t.Errorf("ID = %q, want req-1", req.ID)
	}
	if gotPath != "/fal-ai/flux/dev" {
		t.Errorf("path = %q, want /fal-ai/flux/dev", gotPath)
	}
	if gotWebhook != "https://example.com/hook" {
		t.Errorf("fal_webhook = %q, want https://example.com/hook", gotWebhook)
	}
	if gotPriority != "low" {
		t.Errorf("X-Fal-Queue-Priority = %q, want low", gotPriority)
	}
	if gotHint != "runner-3" {
		t.Errorf("X-Fal-Runner-Hint = %q, want runner-3", gotHint)
	}
}

func TestQueueLifecycle(t *testing.T) {
	var polls atomic.Int32
	var gotLogsQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fal-ai/flux", func(w http.ResponseWriter, r *http.Request) {
		submitBody(w, r, "req-9")
	})
	mux.HandleFunc("GET /requests/req-9/status", func(w http.ResponseWriter, r *http.Request) {
		gotLogsQuery = r.URL.Query().Get("logs")
		w.Header().Set("Content-Type", "application/json")
		switch polls.Add(1) {
		case 1:
			_, _ = w.Write([]byte(`{"status":"IN_QUEUE","queue_position":2}`))
		case 2:
			_, _ = w.Write([]byte(`{"status":"IN_PROGRESS","logs":[{"message":"go"}]}`))
		default:
			_, _ = w.Write([]byte(`{"status":"COMPLETED","metrics":{"t":1}}`))
		}
	})
	mux.HandleFunc("GET /requests/req-9", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image":"final"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	ctx := context.Background()
	req, err := c.Submit(ctx, "fal-ai/flux", nil)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var seen []Status
	for st, err := range req.Events(ctx, WithPollInterval(time.Millisecond), WithLogs()) {
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		seen = append(seen, st)
	}

	if len(seen) != 3 {
		t.Fatalf("saw %d statuses, want 3: %+v", len(seen), seen)
	}
	if q, ok := seen[0].(Queued); !ok || q.Position != 2 {
		t.Errorf("status[0] = %+v, want Queued{Position:2}", seen[0])
	}
	if _, ok := seen[1].(InProgress); !ok {
		t.Errorf("status[1] = %+v, want InProgress", seen[1])
	}
	if _, ok := seen[2].(Completed); !ok {
		t.Errorf("status[2] = %+v, want Completed", seen[2])
	}
	if gotLogsQuery != "true" {
		t.Errorf("logs query = %q, want true", gotLogsQuery)
	}

	res, err := req.Result(ctx)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if out["image"] != "final" {
		t.Errorf("result image = %q, want final", out["image"])
	}
}

func TestStatusLogsQueryFalse(t *testing.T) {
	var gotLogs string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fal-ai/flux", func(w http.ResponseWriter, r *http.Request) {
		submitBody(w, r, "req-2")
	})
	mux.HandleFunc("GET /requests/req-2/status", func(w http.ResponseWriter, r *http.Request) {
		gotLogs = r.URL.Query().Get("logs")
		_, _ = w.Write([]byte(`{"status":"IN_QUEUE","queue_position":1}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	req, err := c.Submit(context.Background(), "fal-ai/flux", nil)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := req.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if gotLogs != "false" {
		t.Errorf("logs query = %q, want false", gotLogs)
	}
}

func TestEventsTerminationOnCompleted(t *testing.T) {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fal-ai/flux", func(w http.ResponseWriter, r *http.Request) {
		submitBody(w, r, "req-c")
	})
	mux.HandleFunc("GET /requests/req-c/status", func(w http.ResponseWriter, r *http.Request) {
		polls.Add(1)
		_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	req, err := c.Submit(context.Background(), "fal-ai/flux", nil)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var count int
	for st, err := range req.Events(context.Background(), WithPollInterval(time.Millisecond)) {
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		count++
		if _, ok := st.(Completed); !ok {
			t.Fatalf("status = %+v, want Completed", st)
		}
	}
	if count != 1 {
		t.Errorf("yielded %d times, want 1 (stop after Completed)", count)
	}
	if got := polls.Load(); got != 1 {
		t.Errorf("polled %d times, want 1", got)
	}
}

func TestEventsTerminationOnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fal-ai/flux", func(w http.ResponseWriter, r *http.Request) {
		submitBody(w, r, "req-e")
	})
	mux.HandleFunc("GET /requests/req-e/status", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"detail":"server on fire"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	req, err := c.Submit(context.Background(), "fal-ai/flux", nil)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	var statuses, errsSeen int
	for st, err := range req.Events(context.Background(), WithPollInterval(time.Millisecond)) {
		if err != nil {
			errsSeen++
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want *APIError", err)
			}
			continue
		}
		if st != nil {
			statuses++
		}
	}
	if statuses != 0 {
		t.Errorf("yielded %d statuses, want 0", statuses)
	}
	if errsSeen != 1 {
		t.Errorf("yielded %d errors, want 1 (stop after error)", errsSeen)
	}
}

func TestSubscribeCallbackOrder(t *testing.T) {
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fal-ai/flux", func(w http.ResponseWriter, r *http.Request) {
		submitBody(w, r, "req-s")
	})
	mux.HandleFunc("GET /requests/req-s/status", func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"status":"IN_QUEUE","queue_position":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"COMPLETED"}`))
	})
	mux.HandleFunc("GET /requests/req-s", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	var events []string
	_, err := c.Subscribe(context.Background(), "fal-ai/flux", nil,
		WithPollInterval(time.Millisecond),
		OnEnqueue(func(id string) { events = append(events, "enqueue:"+id) }),
		OnUpdate(func(s Status) { events = append(events, fmt.Sprintf("update:%T", s)) }),
	)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if len(events) < 2 {
		t.Fatalf("events = %v, want at least enqueue then update", events)
	}
	if events[0] != "enqueue:req-s" {
		t.Errorf("events[0] = %q, want enqueue:req-s", events[0])
	}
	for _, e := range events[1:] {
		if len(e) < 7 || e[:7] != "update:" {
			t.Errorf("event %q after enqueue is not an update", e)
		}
	}
}

func TestSubscribeCtxCancelAutoCancels(t *testing.T) {
	var cancelHit atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fal-ai/flux", func(w http.ResponseWriter, r *http.Request) {
		submitBody(w, r, "req-x")
	})
	mux.HandleFunc("GET /requests/req-x/status", func(w http.ResponseWriter, r *http.Request) {
		// Never completes: always in progress.
		_, _ = w.Write([]byte(`{"status":"IN_PROGRESS"}`))
	})
	mux.HandleFunc("PUT /requests/req-x/cancel", func(w http.ResponseWriter, r *http.Request) {
		cancelHit.Store(true)
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := c.Subscribe(ctx, "fal-ai/flux", nil, WithPollInterval(2*time.Millisecond))
	if err == nil {
		t.Fatal("Subscribe: want error after ctx cancel, got nil")
	}
	var toErr *TimeoutError
	if !errors.As(err, &toErr) {
		t.Fatalf("error = %v, want *TimeoutError", err)
	}
	if toErr.RequestID != "req-x" {
		t.Errorf("RequestID = %q, want req-x", toErr.RequestID)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error does not wrap context.Canceled: %v", err)
	}
	if !cancelHit.Load() {
		t.Error("cancel endpoint was not hit")
	}
}

func TestReattachURLs(t *testing.T) {
	var statusPath string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /fal-ai/flux/requests/abc/status", func(w http.ResponseWriter, r *http.Request) {
		statusPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"IN_QUEUE","queue_position":0}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	req, err := c.Request("fal-ai/flux", "abc")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if req.ID != "abc" {
		t.Errorf("ID = %q, want abc", req.ID)
	}
	if _, err := req.Status(context.Background()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if statusPath != "/fal-ai/flux/requests/abc/status" {
		t.Errorf("status path = %q, want /fal-ai/flux/requests/abc/status", statusPath)
	}
}

func TestReattachEmptyID(t *testing.T) {
	c := testClient("http://example.invalid")
	if _, err := c.Request("fal-ai/flux", ""); err == nil {
		t.Fatal("Request with empty id: want error, got nil")
	}
}

func TestRequestCancel(t *testing.T) {
	var method string
	mux := http.NewServeMux()
	mux.HandleFunc("/fal-ai/flux/requests/abc/cancel", func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	req, err := c.Request("fal-ai/flux", "abc")
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := req.Cancel(context.Background()); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("cancel method = %q, want PUT", method)
	}
}

func TestResultCompletedWithError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /fal-ai/flux", func(w http.ResponseWriter, r *http.Request) {
		submitBody(w, r, "req-f")
	})
	mux.HandleFunc("GET /requests/req-f/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"COMPLETED","error":"model failed","error_type":"ExecutionError"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := testClient(srv.URL)
	req, err := c.Submit(context.Background(), "fal-ai/flux", nil)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	_, err = req.Result(context.Background())
	if err == nil {
		t.Fatal("Result: want error for failed completion, got nil")
	}
}

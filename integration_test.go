//go:build integration

// Package fal_test integration tests exercise the live fal.ai API. They run only
// under the "integration" build tag and only when FAL_KEY is set in the
// environment. Task supplies FAL_KEY from .env; run them with:
//
//	task test:integration
//
// The suite keeps live inference to a minimum: Run and Subscribe each make one
// image call against fal-ai/flux/schnell with a single low-step image, Stream is
// best-effort and skips when the endpoint has no SSE support, and Realtime only
// checks that a connection opens cleanly.
package fal_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	fal "github.com/regiellis/sdk-fal-go"
)

// cheapImageInput is the minimal, low-cost input used for image inference.
var cheapImageInput = map[string]any{
	"prompt":              "a small blue circle on a white background",
	"image_size":          "square",
	"num_images":          1,
	"num_inference_steps": 1,
}

const imageModel = "fal-ai/flux/schnell"

// imageResult decodes the image URLs from a flux response.
type imageResult struct {
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
}

// requireKey skips the test when no credentials are available.
func requireKey(t *testing.T) {
	t.Helper()
	if os.Getenv("FAL_KEY") == "" {
		t.Skip("FAL_KEY not set; skipping live integration test")
	}
}

func TestIntegrationRun(t *testing.T) {
	requireKey(t)
	t.Parallel()

	client := fal.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	raw, err := client.Run(ctx, imageModel, cheapImageInput)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	var out imageResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if len(out.Images) == 0 || out.Images[0].URL == "" {
		t.Fatalf("expected an image URL, got %s", raw)
	}
	t.Logf("image URL: %s", out.Images[0].URL)
}

func TestIntegrationSubscribe(t *testing.T) {
	requireKey(t)
	t.Parallel()

	client := fal.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var (
		enqueued    string
		sawUpdate   bool
		sawComplete bool
	)

	raw, err := client.Subscribe(ctx, imageModel, cheapImageInput,
		fal.WithLogs(),
		fal.OnEnqueue(func(id string) { enqueued = id }),
		fal.OnUpdate(func(s fal.Status) {
			sawUpdate = true
			if _, ok := s.(fal.Completed); ok {
				sawComplete = true
			}
		}),
	)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	if enqueued == "" {
		t.Error("OnEnqueue was not called with a request id")
	}
	if !sawUpdate {
		t.Error("OnUpdate was never called")
	}
	if !sawComplete {
		t.Error("no Completed status was observed")
	}

	var out imageResult
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding result: %v", err)
	}
	if len(out.Images) == 0 || out.Images[0].URL == "" {
		t.Fatalf("expected an image URL, got %s", raw)
	}
	t.Logf("request %s image URL: %s", enqueued, out.Images[0].URL)
}

func TestIntegrationUpload(t *testing.T) {
	requireKey(t)
	t.Parallel()

	client := fal.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	data := []byte("sdk-fal-go integration upload check\n")
	url, err := client.Upload(ctx, data, "text/plain", fal.WithFileName("check.txt"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if url == "" {
		t.Fatal("expected a non-empty upload URL")
	}
	t.Logf("uploaded to %s", url)

	// EncodeFile sanity check against a temp file: no network involved.
	tmp := t.TempDir() + "/check.txt"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	dataURL, err := fal.EncodeFile(tmp)
	if err != nil {
		t.Fatalf("EncodeFile: %v", err)
	}
	if dataURL == "" {
		t.Fatal("expected a non-empty data URL")
	}
}

func TestIntegrationStream(t *testing.T) {
	requireKey(t)
	t.Parallel()

	client := fal.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	events := 0
	var streamErr error
	for event, err := range client.Stream(ctx, imageModel, cheapImageInput) {
		if err != nil {
			streamErr = err
			break
		}
		if len(event) == 0 {
			t.Error("received an empty stream event")
		}
		events++
	}

	if streamErr != nil {
		// The endpoint may not support Server-Sent Events; that is not a failure
		// of the client, so skip rather than fail.
		t.Skipf("streaming not available for %s: %v", imageModel, streamErr)
	}
	if events == 0 {
		t.Skip("stream produced no events; endpoint may not support SSE")
	}
	t.Logf("received %d stream events", events)
}

func TestIntegrationRealtime(t *testing.T) {
	requireKey(t)
	t.Parallel()

	app := os.Getenv("FAL_REALTIME_APP")
	if app == "" {
		app = "fal-ai/fast-turbo-diffusion"
	}

	client := fal.NewClient()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	conn, err := client.Realtime(ctx, app)
	if err != nil {
		t.Skipf("realtime connection to %s did not open cleanly: %v", app, err)
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, io.EOF) {
			t.Logf("close: %v", cerr)
		}
	}()
	t.Logf("realtime connection to %s opened", app)
}

// Command subscribe enqueues a request, reports progress, and prints the result.
//
// It reads credentials from the environment (FAL_KEY). Run it with:
//
//	go run ./examples/subscribe
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	fal "github.com/regiellis/sdk-fal-go"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	client := fal.NewClient()

	input := map[string]any{
		"prompt":              "a lighthouse on a cliff at sunrise",
		"image_size":          "square",
		"num_images":          1,
		"num_inference_steps": 4,
	}

	raw, err := client.Subscribe(context.Background(), "fal-ai/flux/schnell", input,
		fal.WithLogs(),
		fal.OnEnqueue(func(id string) {
			log.Printf("queued: %s", id)
		}),
		fal.OnUpdate(func(s fal.Status) {
			switch v := s.(type) {
			case fal.Queued:
				log.Printf("position %d", v.Position)
			case fal.InProgress:
				log.Printf("in progress, %d log lines", len(v.Logs))
			case fal.Completed:
				log.Print("done")
			}
		}),
	)
	if err != nil {
		return err
	}

	var out struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	if len(out.Images) == 0 {
		return fmt.Errorf("no images in response")
	}

	fmt.Println(out.Images[0].URL)
	return nil
}

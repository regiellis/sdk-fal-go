// Command stream consumes a model's server-sent events and prints each one.
//
// It reads credentials from the environment (FAL_KEY). Run it with:
//
//	go run ./examples/stream
//
// Not every model streams. When the endpoint does not support server-sent
// events, the iterator yields an error.
package main

import (
	"context"
	"fmt"
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
		"prompt":              "a field of tulips under a clear sky",
		"image_size":          "square",
		"num_images":          1,
		"num_inference_steps": 4,
	}

	for event, err := range client.Stream(context.Background(), "fal-ai/flux/schnell", input) {
		if err != nil {
			return err
		}
		fmt.Printf("%s\n", event)
	}
	return nil
}

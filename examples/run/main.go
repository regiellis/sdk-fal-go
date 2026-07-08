// Command run executes a model synchronously and prints the image URL.
//
// It reads credentials from the environment (FAL_KEY). Run it with:
//
//	go run ./examples/run
package main

import (
	"context"
	"encoding/json"
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

	raw, err := client.Run(context.Background(), "fal-ai/flux/schnell", map[string]any{
		"prompt":              "a red panda reading a book in a cozy library",
		"image_size":          "square",
		"num_images":          1,
		"num_inference_steps": 4,
	})
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

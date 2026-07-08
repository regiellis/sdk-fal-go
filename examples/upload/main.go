// Command upload stores a small file in fal storage and prints its URL.
//
// It reads credentials from the environment (FAL_KEY). Run it with:
//
//	go run ./examples/upload
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

	data := []byte("hello from sdk-fal-go\n")

	url, err := client.Upload(context.Background(), data, "text/plain",
		fal.WithFileName("greeting.txt"),
	)
	if err != nil {
		return err
	}

	fmt.Println("uploaded:", url)

	// For small inputs, a data URL avoids the upload entirely and can be passed
	// straight to a model input that accepts a file URL.
	dataURL := fal.Encode(data, "text/plain")
	fmt.Println("data url length:", len(dataURL))
	return nil
}

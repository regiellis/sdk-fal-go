// Command realtime opens a WebSocket connection, sends one input, and prints the
// results until the connection closes.
//
// It reads credentials from the environment (FAL_KEY). Run it with:
//
//	go run ./examples/realtime
//
// Realtime app availability varies. Point APP at a realtime-capable endpoint.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	fal "github.com/regiellis/sdk-fal-go"
)

// app is the realtime endpoint to connect to. Override it with the APP
// environment variable.
const app = "fal-ai/fast-turbo-diffusion"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	target := app
	if v := os.Getenv("APP"); v != "" {
		target = v
	}

	client := fal.NewClient()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := client.Realtime(ctx, target)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Send(ctx, map[string]any{
		"prompt": "a mountain lake reflecting the sky",
	}); err != nil {
		return err
	}

	for {
		msg, err := conn.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		fmt.Printf("%s\n", msg)
	}
}

package fal_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"

	fal "github.com/regiellis/sdk-fal-go"
)

// result is a minimal decode target shared by the examples below.
type result struct {
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
}

func ExampleClient_Run() {
	client := fal.NewClient()

	raw, err := client.Run(context.Background(), "fal-ai/flux/schnell", map[string]any{
		"prompt":              "a red panda reading a book",
		"image_size":          "square",
		"num_images":          1,
		"num_inference_steps": 4,
	})
	if err != nil {
		log.Fatal(err)
	}

	var out result
	if err := json.Unmarshal(raw, &out); err != nil {
		log.Fatal(err)
	}
	fmt.Println(out.Images[0].URL)
}

func ExampleClient_Subscribe() {
	client := fal.NewClient()

	input := map[string]any{
		"prompt":              "a lighthouse on a cliff at sunrise",
		"num_inference_steps": 4,
	}

	raw, err := client.Subscribe(context.Background(), "fal-ai/flux/schnell", input,
		fal.WithLogs(),
		fal.OnUpdate(func(s fal.Status) {
			if _, done := s.(fal.Completed); done {
				log.Print("done")
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	var out result
	if err := json.Unmarshal(raw, &out); err != nil {
		log.Fatal(err)
	}
	fmt.Println(out.Images[0].URL)
}

func ExampleClient_Stream() {
	client := fal.NewClient()

	input := map[string]any{"prompt": "a field of tulips"}

	for event, err := range client.Stream(context.Background(), "fal-ai/flux/schnell", input) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s\n", event)
	}
}

func ExampleClient_Upload() {
	client := fal.NewClient()

	data := []byte("hello from sdk-fal-go")

	url, err := client.Upload(context.Background(), data, "text/plain")
	if err != nil {
		log.Fatal(err)
	}

	// Pass the returned URL as a model input.
	fmt.Println(url)
}

func ExampleClient_Realtime() {
	client := fal.NewClient()

	ctx := context.Background()
	conn, err := client.Realtime(ctx, "fal-ai/some-realtime-app")
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Send(ctx, map[string]any{"prompt": "a mountain lake"}); err != nil {
		log.Fatal(err)
	}

	for {
		msg, err := conn.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s\n", msg)
	}
}

func ExampleEncode() {
	data := []byte("hello")
	dataURL := fal.Encode(data, "text/plain")
	fmt.Println(dataURL)
	// Output: data:text/plain;base64,aGVsbG8=
}

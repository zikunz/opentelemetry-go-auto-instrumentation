// Copyright (c) 2026 Alibaba Group Holding Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/ollama/ollama/api"
)

func main() {
	// Reads OLLAMA_HOST (default http://127.0.0.1:11434).
	client, err := api.ClientFromEnvironment()
	if err != nil {
		log.Fatalf("failed to create ollama client: %v", err)
	}

	model := os.Getenv("OLLAMA_MODEL")
	if model == "" {
		model = "tinyllama"
	}

	ctx := context.Background()

	fmt.Println("=== Chat ===")
	chat(ctx, client, model)

	fmt.Println("\n=== Streaming Chat ===")
	chatStream(ctx, client, model)

	fmt.Println("\n=== Generate ===")
	generate(ctx, client, model)

	fmt.Println("\nDone!")
}

func chat(ctx context.Context, client *api.Client, model string) {
	stream := false
	req := &api.ChatRequest{
		Model: model,
		Messages: []api.Message{
			{Role: "user", Content: "In one short sentence, what is OpenTelemetry?"},
		},
		Stream: &stream,
	}
	err := client.Chat(ctx, req, func(resp api.ChatResponse) error {
		fmt.Printf("Response: %s\n", resp.Message.Content)
		return nil
	})
	if err != nil {
		log.Printf("chat failed: %v", err)
	}
}

func chatStream(ctx context.Context, client *api.Client, model string) {
	req := &api.ChatRequest{
		Model: model,
		Messages: []api.Message{
			{Role: "user", Content: "Say hello in three words."},
		},
	}
	err := client.Chat(ctx, req, func(resp api.ChatResponse) error {
		fmt.Print(resp.Message.Content)
		if resp.Done {
			fmt.Println()
		}
		return nil
	})
	if err != nil {
		log.Printf("streaming chat failed: %v", err)
	}
}

func generate(ctx context.Context, client *api.Client, model string) {
	stream := false
	req := &api.GenerateRequest{
		Model:  model,
		Prompt: "Complete this sentence in a few words: observability is",
		Stream: &stream,
	}
	err := client.Generate(ctx, req, func(resp api.GenerateResponse) error {
		fmt.Printf("Response: %s\n", resp.Response)
		return nil
	})
	if err != nil {
		log.Printf("generate failed: %v", err)
	}
}

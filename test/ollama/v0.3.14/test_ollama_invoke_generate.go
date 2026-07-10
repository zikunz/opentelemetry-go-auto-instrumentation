// Copyright (c) 2025 Alibaba Group Holding Ltd.
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
	"encoding/json"
	"fmt"
	"time"

	"github.com/alibaba/loongsuite-go/test/verifier"
	"github.com/ollama/ollama/api"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func main() {
	ctx := context.Background()
	client, server := NewMockOllamaGenerateForInvoke(ctx)
	defer server.Close()
	streamFlag := false
	req := &api.GenerateRequest{
		Model:  "llama3:8b",
		Prompt: "Hello",
		Stream: &streamFlag,
	}
	err := client.Generate(ctx, req, func(resp api.GenerateResponse) error {
		return nil
	})
	if err != nil {
		panic(err)
	}

	// A response truncated by the token limit must surface the real
	// done_reason instead of the "stop" fallback.
	truncatedClient, truncatedServer := SetupMockGenerate(api.GenerateResponse{
		Model:      "llama3:8b",
		CreatedAt:  time.Now(),
		Response:   "truncated output",
		Done:       true,
		DoneReason: "length",
		Metrics: api.Metrics{
			PromptEvalCount: 5,
			EvalCount:       7,
		},
	})
	defer truncatedServer.Close()
	err = truncatedClient.Generate(ctx, &api.GenerateRequest{
		Model:  "llama3:8b",
		Prompt: "Hello",
		Stream: &streamFlag,
	}, func(resp api.GenerateResponse) error {
		return nil
	})
	if err != nil {
		panic(err)
	}

	verifier.WaitAndAssertTraces(func(stubs []tracetest.SpanStubs) {
		verifier.VerifyLLMAttributes(stubs[0][0], "generate", "ollama", "llama3:8b")
		input, _ := getAttributeValue(stubs[0][0], "gen_ai.input.messages").(string)
		if input == "" {
			panic("gen_ai.input.messages not found on generate span")
		}
		var messages []struct {
			Role  string `json:"role"`
			Parts []struct {
				Type    string `json:"type"`
				Content string `json:"content"`
			} `json:"parts"`
		}
		if err := json.Unmarshal([]byte(input), &messages); err != nil || len(messages) != 1 ||
			messages[0].Role != "user" || len(messages[0].Parts) != 1 ||
			messages[0].Parts[0].Type != "text" || messages[0].Parts[0].Content != "Hello" {
			panic(fmt.Sprintf("unexpected gen_ai.input.messages: %s", input))
		}

		foundLength := false
		for _, trace := range stubs {
			for _, span := range trace {
				if reasons, ok := getAttributeValue(span, "gen_ai.response.finish_reasons").([]string); ok {
					for _, r := range reasons {
						if r == "length" {
							foundLength = true
						}
					}
				}
			}
		}
		if !foundLength {
			panic(`expected a span with gen_ai.response.finish_reasons containing "length" (DoneReason not captured)`)
		}
	}, 2)
}

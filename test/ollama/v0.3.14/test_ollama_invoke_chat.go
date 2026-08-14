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

	"github.com/alibaba/loongsuite-go/test/verifier"
	"github.com/ollama/ollama/api"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func main() {
	ctx := context.Background()
	client, server := NewMockOllamaChatForInvoke(ctx)
	defer server.Close()
	streamFlag := false
	req := &api.ChatRequest{
		Model: "llama3:8b",
		Messages: []api.Message{
			{Role: "user", Content: "Hello"},
		},
		Stream: &streamFlag,
	}
	err := client.Chat(ctx, req, func(resp api.ChatResponse) error {
		return nil
	})
	if err != nil {
		panic(err)
	}
	verifier.WaitAndAssertTraces(func(stubs []tracetest.SpanStubs) {
		verifier.VerifyLLMAttributes(stubs[0][0], "chat", "ollama", "llama3:8b")
		input, _ := getAttributeValue(stubs[0][0], "gen_ai.input.messages").(string)
		if input == "" {
			panic("gen_ai.input.messages not found on chat span")
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
	}, 1)
}

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
	"fmt"
	"time"

	"github.com/alibaba/loongsuite-go/test/verifier"
	"github.com/ollama/ollama/api"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func main() {
	ctx := context.Background()
	client, server := NewMockOllamaChatForInvoke(ctx)
	defer server.Close()
	streamFlag := false
	req := &api.ChatRequest{Model: "llama3:70b", Messages: []api.Message{{Role: "user", Content: "Hello"}}, Stream: &streamFlag}
	_ = client.Chat(ctx, req, func(resp api.ChatResponse) error { return nil })

	// Second request, blocking: the reply is cut off by the token limit, done_reason "length".
	truncatedClient, truncatedServer := SetupMockChat(api.ChatResponse{
		Model:      "llama3:70b",
		CreatedAt:  time.Now(),
		Message:    api.Message{Role: "assistant", Content: "truncated output"},
		Done:       true,
		DoneReason: "length",
		Metrics: api.Metrics{
			PromptEvalCount: 5,
			EvalCount:       7,
		},
	})
	defer truncatedServer.Close()
	err := truncatedClient.Chat(ctx, &api.ChatRequest{Model: "llama3:70b", Messages: []api.Message{{Role: "user", Content: "Hello"}}, Stream: &streamFlag}, func(resp api.ChatResponse) error { return nil })
	if err != nil {
		panic(err)
	}

	verifier.WaitAndAssertTraces(func(stubs []tracetest.SpanStubs) {
		verifier.VerifyLLMAttributes(stubs[0][0], "chat", "ollama", "llama3:70b")

		reasons, _ := getAttributeValue(stubs[1][0], "gen_ai.response.finish_reasons").([]string)
		if len(reasons) != 1 || reasons[0] != "length" {
			panic(fmt.Sprintf("gen_ai.response.finish_reasons = %v, want [length]", reasons))
		}
	}, 2)
}

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

	// Second request: the reply is cut off by the token limit, done_reason "length".
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
		want := `[{"role":"user","parts":[{"type":"text","content":"Hello"}]}]`
		if input != want {
			panic(fmt.Sprintf("gen_ai.input.messages = %q, want %q", input, want))
		}

		reasons, _ := getAttributeValue(stubs[1][0], "gen_ai.response.finish_reasons").([]string)
		if len(reasons) != 1 || reasons[0] != "length" {
			panic(fmt.Sprintf("gen_ai.response.finish_reasons = %v, want [length]", reasons))
		}
	}, 2)
}

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

	// Second request: a conversation with an image, tool calls and a tool
	// result. The image bytes must not end up in gen_ai.input.messages.
	err = client.Chat(ctx, &api.ChatRequest{
		Model: "llama3:8b",
		Messages: []api.Message{
			{Role: "user", Content: "What is the weather in Paris?", Images: []api.ImageData{{0x00, 0x01, 0x02, 0xff}}},
			{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{
				Name:      "get_weather",
				Arguments: api.ToolCallFunctionArguments{"city": "Paris"},
			}}}},
			{Role: "tool", Content: "sunny"},
			{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "no_args"}}}},
		},
		Stream: &streamFlag,
	}, func(resp api.ChatResponse) error {
		return nil
	})
	if err != nil {
		panic(err)
	}

	verifier.WaitAndAssertTraces(func(stubs []tracetest.SpanStubs) {
		verifier.VerifyLLMAttributes(stubs[0][0], "chat", "ollama", "llama3:8b")
		input, _ := getAttributeValue(stubs[0][0], "gen_ai.input.messages").(string)
		want := `[{"role":"user","parts":[{"type":"text","content":"Hello"}]}]`
		if input != want {
			panic(fmt.Sprintf("gen_ai.input.messages = %q, want %q", input, want))
		}

		input, _ = getAttributeValue(stubs[1][0], "gen_ai.input.messages").(string)
		want = `[{"role":"user","parts":[{"type":"text","content":"What is the weather in Paris?"}]},` +
			`{"role":"assistant","parts":[{"type":"tool_call","name":"get_weather","arguments":{"city":"Paris"}}]},` +
			`{"role":"tool","parts":[{"type":"tool_call_response","response":"sunny"}]},` +
			`{"role":"assistant","parts":[{"type":"tool_call","name":"no_args"}]}]`
		if input != want {
			panic(fmt.Sprintf("gen_ai.input.messages = %q, want %q", input, want))
		}
	}, 2)
}

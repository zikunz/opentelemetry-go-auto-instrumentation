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

package ollama

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	ollamaapi "github.com/ollama/ollama/api"
)

func TestSerializeInputMessagesEmpty(t *testing.T) {
	for _, messages := range [][]ollamaapi.Message{nil, []ollamaapi.Message{}} {
		if got := serializeInputMessages(messages); got != "" {
			t.Fatalf("serializeInputMessages(%v) = %q, want empty", messages, got)
		}
	}
}

func TestSerializeInputMessagesSemanticConventionShape(t *testing.T) {
	content := "line \"one\"\n你好 <tag>"
	raw := serializeInputMessages([]ollamaapi.Message{{Role: "user", Content: content}})

	var messages []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &messages); err != nil {
		t.Fatalf("invalid JSON %q: %v", raw, err)
	}
	if len(messages) != 1 {
		t.Fatalf("got %d messages, want 1: %s", len(messages), raw)
	}
	if _, ok := messages[0]["content"]; ok {
		t.Fatalf("top-level content is not part of the semantic-convention schema: %s", raw)
	}
	if _, ok := messages[0]["tool_calls"]; ok {
		t.Fatalf("top-level tool_calls is not part of the semantic-convention schema: %s", raw)
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(messages[0]["parts"], &parts); err != nil || len(parts) != 1 {
		t.Fatalf("invalid text parts in %s: %v", raw, err)
	}
	if _, ok := parts[0]["arguments"]; ok {
		t.Fatalf("text part contains tool-call arguments: %s", raw)
	}

	var decoded []inputMessage
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded) != 1 || decoded[0].Role != "user" || len(decoded[0].Parts) != 1 {
		t.Fatalf("unexpected message shape: %#v", decoded)
	}
	part := decoded[0].Parts[0]
	if part.Type != "text" || part.Content == nil || *part.Content != content {
		t.Fatalf("unexpected text part: %#v", part)
	}
}

func TestSerializeInputMessagesToolCallsAndImages(t *testing.T) {
	raw := serializeInputMessages([]ollamaapi.Message{{
		Role:    "assistant",
		Content: "calling tool",
		Images:  []ollamaapi.ImageData{{0x00, 0x01, 0x02, 0xff}},
		ToolCalls: []ollamaapi.ToolCall{{Function: ollamaapi.ToolCallFunction{
			Name:      "lookup",
			Arguments: makeToolArguments(t, "city", "Paris"),
		}}},
	}})
	if strings.Contains(raw, "images") || strings.Contains(raw, "AAEC/w==") {
		t.Fatalf("raw image data leaked into input messages: %s", raw)
	}

	var decoded []inputMessage
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded) != 1 || len(decoded[0].Parts) != 2 {
		t.Fatalf("unexpected message shape: %#v", decoded)
	}
	toolPart := decoded[0].Parts[1]
	var arguments map[string]any
	if err := json.Unmarshal(toolPart.Arguments, &arguments); err != nil {
		t.Fatalf("decode tool-call arguments: %v", err)
	}
	if toolPart.Type != "tool_call" || toolPart.Name == nil || *toolPart.Name != "lookup" ||
		arguments["city"] != "Paris" {
		t.Fatalf("unexpected tool-call part: %#v", toolPart)
	}
}

func TestSerializeInputMessagesEmptyToolArguments(t *testing.T) {
	raw := serializeInputMessages([]ollamaapi.Message{{
		Role: "assistant",
		ToolCalls: []ollamaapi.ToolCall{{Function: ollamaapi.ToolCallFunction{
			Name: "no_args",
		}}},
	}})
	if strings.Contains(raw, `"arguments"`) {
		t.Fatalf("empty tool-call arguments should be omitted: %s", raw)
	}
}

func TestSerializeInputMessagesImageOnlyKeepsPartsArray(t *testing.T) {
	raw := serializeInputMessages([]ollamaapi.Message{{
		Role:   "user",
		Images: []ollamaapi.ImageData{{0x00}},
	}})
	if raw != `[{"role":"user","parts":[]}]` {
		t.Fatalf("image-only message = %s, want role with an empty parts array", raw)
	}
}

func TestSerializeInputMessagesMarshalFailure(t *testing.T) {
	raw := serializeInputMessages([]ollamaapi.Message{{
		Role: "assistant",
		ToolCalls: []ollamaapi.ToolCall{{Function: ollamaapi.ToolCallFunction{
			Name:      "invalid",
			Arguments: makeToolArguments(t, "unsupported", func() {}),
		}}},
	}})
	if raw != "" {
		t.Fatalf("marshal failure = %q, want empty", raw)
	}
}

// makeToolArguments supports both the map used in Ollama v0.3.14 and the
// ordered-map-backed struct used by newer releases. The rule targets [0.3.14,).
func makeToolArguments(t *testing.T, key string, value any) ollamaapi.ToolCallFunctionArguments {
	t.Helper()
	var arguments ollamaapi.ToolCallFunctionArguments
	argumentsValue := reflect.ValueOf(&arguments).Elem()
	if argumentsValue.Kind() == reflect.Map {
		argumentsValue.Set(reflect.MakeMap(argumentsValue.Type()))
		argumentsValue.SetMapIndex(reflect.ValueOf(key), reflect.ValueOf(value))
		return arguments
	}
	setMethod := reflect.ValueOf(&arguments).MethodByName("Set")
	if !setMethod.IsValid() {
		t.Fatalf("unsupported ToolCallFunctionArguments type: %T", arguments)
	}
	setMethod.Call([]reflect.Value{reflect.ValueOf(key), reflect.ValueOf(value)})
	return arguments
}

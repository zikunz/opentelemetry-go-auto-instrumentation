# Ollama Demo

This demo shows how to use loongsuite-go to automatically instrument
[Ollama](https://github.com/ollama/ollama) API calls with OpenTelemetry — no
code changes required. It covers chat, streaming chat, and generate, producing
spans and metrics that follow the OpenTelemetry GenAI semantic conventions.

## How to run it?

### 1. Build agent

Go to the root directory of `loongsuite-go` and execute:

```shell
make clean && make build
```

### 2. Do hybrid compilation

```shell
cd example/ollama-demo
../../otel go build
```

### 3. Run Ollama and pull a model

If you don't have Ollama installed locally, you can run it with Docker:

```shell
docker run -d --name ollama -p 11434:11434 ollama/ollama
docker exec ollama ollama pull tinyllama
```

Or, with a native install: `ollama pull tinyllama`.

### 4. Run Jaeger (optional, for viewing traces)

```shell
docker run --rm -d --name jaeger \
  -p 16686:16686 \
  -p 4317:4317 \
  -p 4318:4318 \
  jaegertracing/all-in-one:1.53.0
```

### 5. Run the demo

```shell
OTEL_EXPORTER_OTLP_ENDPOINT="http://127.0.0.1:4318" \
OTEL_EXPORTER_OTLP_INSECURE=true \
OTEL_METRICS_EXPORTER=console \
OTEL_SERVICE_NAME=ollama-demo \
./ollama-demo
```

### 6. Check trace data

Access Jaeger UI: http://localhost:16686

The command above sends traces to Jaeger and prints metrics as JSON to standard
output. Look for the metric names listed below in the console output.

You should see one GenAI span per model call: two `chat` spans and one
`generate` span. Each includes:

- `gen_ai.operation.name`, `gen_ai.request.model`, `gen_ai.system`
- Token usage (`gen_ai.usage.input_tokens` / `gen_ai.usage.output_tokens`)
- `gen_ai.response.finish_reasons` and `server.address`
- Streaming requests additionally record time-to-first-token in the
  `gen_ai.server.time_to_first_token` metric, alongside
  `gen_ai.client.operation.duration` and `gen_ai.client.token.usage`

| Environment Variable | Description                                  | Example                    |
|----------------------|----------------------------------------------|----------------------------|
| OLLAMA_HOST          | Ollama server address (optional)             | http://127.0.0.1:11434     |
| OLLAMA_MODEL         | Model used by the demo (default `tinyllama`) | llama3:8b                  |

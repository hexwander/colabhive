package server

import (
	"net/http"

	"github.com/colabhive/colabhive/internal/llmgw"
)

// RegisterV11LLMRoutes mounts ColabHive v1.1 Pillar 1 Private AI Chat & Zero-Tunnel
// OpenAI (`/v1/*`), Anthropic (`/v1/messages`), and Ollama (`/api/*`) gateway endpoints onto mux:
//   - GET  /api/v1/llm/catalog
//   - GET  /api/v1/llm/status
//   - POST /api/v1/llm/start
//   - POST /api/v1/llm/stop
//   - POST /api/v1/llm/chat/completions
//   - GET  /v1/models
//   - POST /v1/chat/completions
//   - POST /v1/messages
//   - GET  /api/tags
//   - POST /api/chat
//   - POST /api/generate
func RegisterV11LLMRoutes(mux *http.ServeMux, svc *llmgw.Service) {
	if mux == nil {
		return
	}
	llmgw.RegisterRoutes(mux, svc)
}

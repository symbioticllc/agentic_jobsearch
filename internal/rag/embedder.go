package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const ollamaEmbedURL = "http://localhost:11434/api/embed"

// embedder handles generating vector embeddings via Ollama
type embedder struct {
	client *http.Client
	model  string
}

func newEmbedder(model string) *embedder {
	return &embedder{
		client: &http.Client{Timeout: 60 * time.Second},
		model:  model,
	}
}

type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// sanitizeAndLimit forcefully strips weird unicode, control characters, and enforces length limits
func sanitizeAndLimit(s string) string {
	s = strings.ToValidUTF8(s, " ")
	reg := regexp.MustCompile(`[^\x09\x0A\x0D\x20-\x7E]+`)
	s = reg.ReplaceAllString(s, " ")
	if len(s) > 20000 {
		s = s[:20000]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		s = "empty"
	}
	return s
}

// EmbedText generates a single embedding vector for the provided text
func (e *embedder) EmbedText(ctx context.Context, text string) ([]float32, error) {
	cleanText := sanitizeAndLimit(text)
	body, _ := json.Marshal(embedRequest{Model: e.model, Input: cleanText})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ollamaEmbedURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed request failed: %w", err)
	}
	defer resp.Body.Close()

	var result embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("embed decode error: %w", err)
	}
	if len(result.Embeddings) == 0 || len(result.Embeddings[0]) == 0 {
		return nil, fmt.Errorf("empty embedding returned for model %s", e.model)
	}
	return result.Embeddings[0], nil
}

// EmbedChunks generates embeddings for a batch of chunks, returning embedded chunks
func (e *embedder) EmbedChunks(ctx context.Context, chunks []Chunk) ([]Chunk, error) {
	embedded := make([]Chunk, 0, len(chunks))
	for i, chunk := range chunks {
		vec, err := e.EmbedText(ctx, chunk.Header+"\n"+chunk.Content)
		if err != nil {
			fmt.Printf("   ⚠️  Skipping chunk %d (%q): %v\n", i+1, chunk.Header, err)
			continue
		}
		chunk.Embedding = vec
		embedded = append(embedded, chunk)
		fmt.Printf("   [%d/%d] Embedded: %q (%d dims)\n", i+1, len(chunks), chunk.Header, len(vec))
	}
	return embedded, nil
}

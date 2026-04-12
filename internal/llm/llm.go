package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
)

// localModel is the single local Ollama model used for all generation tiers.
// gemma4:31b — 31B parameter multimodal model, confirmed loaded on this machine.
// Switch to qwen3:30b-a3b once pulled: ollama pull qwen3:30b-a3b
const localModelName = "gemma4:31b"

var localLLM llms.Model

// InitLLM sets up Ollama with a single local model (qwen3:30b-a3b) plus optional cloud failovers.
func InitLLM() error {
	fmt.Println(" -> Initializing LLM Orchestration Layer (Single-Model Architecture)...")

	var err error

	// Single local model: Qwen3 30B-A3B handles all tasks — fast extraction through deep tailoring.
	fmt.Printf("    - Loading Local Model (%s)...\n", localModelName)
	localLLM, err = ollama.New(
		ollama.WithServerURL(ollamaServerURL),
		ollama.WithModel(localModelName),
	)
	if err != nil {
		fmt.Printf("      ⚠️  Local model initialization failed: %v. Will failover to Cloud.\n", err)
	}

	// Note: We skip blocking connectivity tests here to avoid long hangs on startup.
	// Connectivity will be verified lazily during the first request.
	if os.Getenv("GEMINI_API_KEY") != "" {
		fmt.Println(" ✅ Cloud Fast Pass (Gemini) integrated.")
	}

	return nil
}

// ollamaServerURL returns the configured Ollama server base URL.
const ollamaServerURL = "http://127.0.0.1:11434"

// PreloadModels fires a no-prompt warm-up request for the local Ollama model
// with keep_alive=-1 so it stays pinned in GPU/CPU memory indefinitely.
// This is especially important when model weights live on a NAS, where the
// first cold-load over the network would otherwise stall the first real request.
// Call this once after InitLLM(); it runs in the background and logs the outcome.
func PreloadModels() {
	models := []struct {
		name  string
		label string
	}{
		{localModelName, localModelName},
	}

	for _, m := range models {
		go func(modelName, label string) {
			fmt.Printf("    🔥 Preloading %s from NAS into memory (keep_alive=-1)...\n", label)
			start := time.Now()

			payload := map[string]interface{}{
				"model":      modelName,
				"keep_alive": -1, // never auto-unload
			}
			body, err := json.Marshal(payload)
			if err != nil {
				log.Printf("⚠️  Preload marshal error for %s: %v", label, err)
				return
			}

			// Use a generous timeout — NAS load of a 30B model can take a minute+
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()

			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				ollamaServerURL+"/api/generate", bytes.NewReader(body))
			if err != nil {
				log.Printf("⚠️  Preload request build error for %s: %v", label, err)
				return
			}
			req.Header.Set("Content-Type", "application/json")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				log.Printf("⚠️  Preload failed for %s: %v", label, err)
				return
			}
			resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				log.Printf("⚠️  Preload got non-200 for %s: %d", label, resp.StatusCode)
				return
			}

			fmt.Printf("    ✅ %s preloaded and pinned in memory (%.1fs)\n", label, time.Since(start).Seconds())
		}(m.name, m.label)
	}
}

// ModelClass defines the priority level of the generation task
type ModelClass int

const (
	ClassFast ModelClass = iota
	ClassDeep
)

// Generate sends a prompt to the appropriate LLM.
// All local generation goes through qwen3:30b-a3b; cloud providers serve as failover.
func Generate(ctx context.Context, class ModelClass, prompt string) (string, error) {
	// Try local Qwen3 first (handles both fast and deep tasks)
	if localLLM != nil {
		resp, err := localLLM.Call(ctx, prompt)
		if err == nil {
			return resp, nil
		}
		log.Printf("⚠️  Local LLM (%s) failed, attempting Cloud failover: %v\n", localModelName, err)
	}

	// Cloud failover 1: Gemini (fast, reliable)
	if os.Getenv("GEMINI_API_KEY") != "" {
		resp, err := generateGemini(ctx, prompt)
		if err == nil {
			return resp, nil
		}
		log.Printf("⚠️  Gemini failover failed: %v\n", err)
	}

	// Cloud failover 2: Claude (high precision)
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		resp, err := generateClaude(ctx, prompt)
		if err == nil {
			return resp, nil
		}
		log.Printf("⚠️  Claude failover failed: %v\n", err)
	}

	return "", fmt.Errorf("no LLM providers available for generation")
}

// generateClaude directly contacts anthropic using minimal standard-lib HTTP structs
func generateClaude(ctx context.Context, prompt string) (string, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}

	payload := map[string]interface{}{
		"model":      "claude-3-5-sonnet-20241022",
		"max_tokens": 4096,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}

	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("claude API request dropped: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("claude API rejected request with status: %d", resp.StatusCode)
	}

	var result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode claude JSON response: %w", err)
	}

	if len(result.Content) == 0 {
		return "", fmt.Errorf("claude API returned an empty text content array")
	}

	return result.Content[0].Text, nil
}

// generateGemini contacts Google AI Studio (Gemini 1.5 Flash)
func generateGemini(ctx context.Context, prompt string) (string, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=%s", apiKey)

	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]interface{}{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]interface{}{
			"temperature": 0.1,
			"topK":        1,
			"topP":        1,
			"maxOutputTokens": 4096,
		},
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Gemini API rejected request with status: %d", resp.StatusCode)
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("Gemini API returned empty response")
	}

	return result.Candidates[0].Content.Parts[0].Text, nil
}

// ExtractedJob holds the structured JSON output from the LLM HTML extraction wrapper
type ExtractedJob struct {
	Title       string `json:"title"`
	Location    string `json:"location"`
	Description string `json:"description"`
}

// ExtractJobsFromText uses the LLM to process unstructured markdown/text from a career page and return jobs in JSON format.
func ExtractJobsFromText(ctx context.Context, company string, text string) ([]ExtractedJob, error) {
	if localLLM == nil {
		return nil, fmt.Errorf("LLM not initialized")
	}

	if len(text) > 8000 {
		text = text[:8000] // truncate to fit context window reasonably
	}

	prompt := fmt.Sprintf(`
You are an intelligent data extraction agent. 
Analyze the following unstructured text extracted from the career page of "%s".
Extract all open software engineering, data, and technical job roles.

Return the results ONLY as a valid JSON array of objects. 
Each object must have exactly these keys: "title", "location", "description".
If you do not find any relevant jobs, return an empty array [].

Text to analyze:
---
%s
---
Output JSON array:`, company, text)

	response, err := Generate(ctx, ClassFast, prompt)
	if err != nil {
		return nil, err
	}

	// Clean up LLM output (remove markdown blocks if present)
	cleaned := strings.TrimSpace(response)
	if strings.HasPrefix(cleaned, "```json") {
		cleaned = strings.TrimPrefix(cleaned, "```json")
		cleaned = strings.TrimSuffix(cleaned, "```")
	} else if strings.HasPrefix(cleaned, "```") {
		cleaned = strings.TrimPrefix(cleaned, "```")
		cleaned = strings.TrimSuffix(cleaned, "```")
	}
	cleaned = strings.TrimSpace(cleaned)

	var jobs []ExtractedJob
	if err := json.Unmarshal([]byte(cleaned), &jobs); err != nil {
		return nil, fmt.Errorf("failed to parse extracted JSON: %w (Raw: %s)", err, cleaned)
	}

	return jobs, nil
}


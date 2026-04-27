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
	"sync"
	"time"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/ollama"
)

// Two-tier model architecture:
//   ClassFast → fastModelName (gemma4:e4b)  — bulk extraction, pre-scoring
//   ClassDeep → deepModelName (gemma4:e4b) — resume tailoring, cover letters
const (
	DefaultFastModel = "gemma4:e4b"
	DefaultDeepModel = "gemma4:e4b"
)

var (
	fastModelName = DefaultFastModel
	deepModelName = DefaultDeepModel

	modelMu sync.RWMutex  // guards fast/deep LLM instances during hot-swap
	fastLLM llms.Model    // lightweight: job extraction from HTML, pre-scoring
	deepLLM llms.Model    // heavyweight: resume tailoring, cover letters, detailed scoring
)

// ActiveModel returns a description of the two-tier configuration.
func ActiveModel() string {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return fmt.Sprintf("%s (fast) / %s (deep)", fastModelName, deepModelName)
}

// ActiveModelNames returns the individual fast and deep model names.
func ActiveModelNames() (fast, deep string) {
	modelMu.RLock()
	defer modelMu.RUnlock()
	return fastModelName, deepModelName
}

// InitLLM sets up both Ollama tiers plus optional cloud failovers.
func InitLLM() error {
	fmt.Println(" -> Initializing LLM Orchestration Layer (Two-Tier Architecture)...")

	var err error

	// Fast tier: qwen3:8b — used for HTML extraction and bulk pre-scoring
	fmt.Printf("    - Loading Fast Model (%s)...\n", fastModelName)
	fastLLM, err = ollama.New(
		ollama.WithServerURL(ollamaServerURL),
		ollama.WithModel(fastModelName),
		ollama.WithRunnerNumCtx(8192),
	)
	if err != nil {
		fmt.Printf("      ⚠️  Fast model initialization failed: %v\n", err)
	}

	// Deep tier: qwen3:30b-a3b — used for full resume tailoring
	fmt.Printf("    - Loading Deep Model (%s)...\n", deepModelName)
	deepLLM, err = ollama.New(
		ollama.WithServerURL(ollamaServerURL),
		ollama.WithModel(deepModelName),
		ollama.WithRunnerNumCtx(16384),
	)
	if err != nil {
		fmt.Printf("      ⚠️  Deep model initialization failed: %v. Will failover to Cloud.\n", err)
	}

	if os.Getenv("GEMINI_API_KEY") != "" {
		fmt.Println(" ✅ Cloud Fast Pass (Gemini) integrated as failover.")
	}

	return nil
}

// ollamaServerURL returns the configured Ollama server base URL.
const ollamaServerURL = "http://127.0.0.1:11434"

// preloadModel fires a no-prompt warm-up for a single Ollama model with keep_alive=-1.
func preloadModel(modelName string, numCtx int) {
	fmt.Printf("    🔥 Preloading %s from NAS into memory (keep_alive=-1)...\n", modelName)
	start := time.Now()

	payload := map[string]interface{}{
		"model":      modelName,
		"keep_alive": -1,
		"options": map[string]interface{}{
			"num_ctx": numCtx,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("⚠️  Preload marshal error for %s: %v", modelName, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ollamaServerURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		log.Printf("⚠️  Preload request build error for %s: %v", modelName, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("⚠️  Preload failed for %s: %v", modelName, err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("⚠️  Preload got non-200 for %s: %d", modelName, resp.StatusCode)
		return
	}

	fmt.Printf("    ✅ %s preloaded and pinned in memory (%.1fs)\n", modelName, time.Since(start).Seconds())
}

// PreloadModels warms up both the fast and deep models in parallel.
func PreloadModels() {
	go preloadModel(fastModelName, 8192)
	go preloadModel(deepModelName, 16384)
}

// ModelClass defines the priority level of the generation task.
type ModelClass int

const (
	ClassFast ModelClass = iota // Use fast lightweight model (qwen3:8b)
	ClassDeep                   // Use deep heavyweight model (qwen3:30b-a3b)
)

// Generate routes the prompt to the correct LLM tier with cloud failover.
func Generate(ctx context.Context, class ModelClass, prompt string) (string, error) {
	// Grab a snapshot of the current models under read lock
	modelMu.RLock()
	var primaryLLM llms.Model
	var primaryName string
	if class == ClassFast {
		primaryLLM = fastLLM
		primaryName = fastModelName
	} else {
		primaryLLM = deepLLM
		primaryName = deepModelName
	}
	modelMu.RUnlock()

	// Try the appropriate local model first
	if primaryLLM != nil {
		resp, err := primaryLLM.Call(ctx, prompt)
		if err == nil {
			return resp, nil
		}
		log.Printf("⚠️  Local LLM (%s) failed, attempting Cloud failover: %v\n", primaryName, err)
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
	if fastLLM == nil {
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

// ListAvailableModels queries the local Ollama server and returns installed model names.
func ListAvailableModels() ([]string, error) {
	resp, err := http.Get(ollamaServerURL + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("ollama unreachable: %w", err)
	}
	defer resp.Body.Close()

	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return nil, fmt.Errorf("failed to decode model list: %w", err)
	}

	var names []string
	for _, m := range tags.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// ConfigureModels hot-swaps both LLM tier clients and triggers a background preload.
// Safe to call at runtime without restarting the server.
func ConfigureModels(newFast, newDeep string) error {
	if newFast == "" || newDeep == "" {
		return fmt.Errorf("both fast and deep model names are required")
	}

	// Build new clients
	newFastLLM, err := ollama.New(
		ollama.WithServerURL(ollamaServerURL),
		ollama.WithModel(newFast),
		ollama.WithRunnerNumCtx(8192),
	)
	if err != nil {
		return fmt.Errorf("failed to init fast model %q: %w", newFast, err)
	}

	newDeepLLM, err := ollama.New(
		ollama.WithServerURL(ollamaServerURL),
		ollama.WithModel(newDeep),
		ollama.WithRunnerNumCtx(16384),
	)
	if err != nil {
		return fmt.Errorf("failed to init deep model %q: %w", newDeep, err)
	}

	// Atomically swap under write lock
	modelMu.Lock()
	fastModelName = newFast
	deepModelName = newDeep
	fastLLM = newFastLLM
	deepLLM = newDeepLLM
	modelMu.Unlock()

	fmt.Printf(" ✅ Models hot-swapped: fast=%s deep=%s\n", newFast, newDeep)

	// Background preload both into GPU/RAM memory
	go preloadModel(newFast, 8192)
	go preloadModel(newDeep, 16384)

	return nil
}

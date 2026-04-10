package rag

import "time"

// Chunk represents a semantically meaningful section of a source document
type Chunk struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`    // filename of origin document
	Header    string    `json:"header"`    // markdown header this chunk falls under
	Content   string    `json:"content"`   // raw text content
	Embedding []float32 `json:"embedding"` // vector from Ollama
	CreatedAt time.Time `json:"created_at"`
}

// QueryResult is a chunk returned from a ChromaDB similarity search
type QueryResult struct {
	Chunk    Chunk
	Distance float64
}

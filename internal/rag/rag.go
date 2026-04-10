package rag

import (
	"context"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/leee/agentic-jobs/internal/scraper"
)

const embeddingModel = "nomic-embed-text"

// RAG is the top-level retrieval-augmented generation orchestrator
type RAG struct {
	redis    *redisClient
	embedder *embedder
}

// InitRedis verifies Redis connectivity and returns a ready RAG instance
func InitRedis() error {
	fmt.Println(" -> Initializing RAG (Redis-Stack) connection...")
	client := newRedisClient()
	if err := client.Heartbeat(context.Background()); err != nil {
		return fmt.Errorf("Redis not reachable: %w", err)
	}
	fmt.Println(" ✅ Redis (Vector Store) connected successfully!")
	return nil
}

// New creates a fully initialized RAG instance, ready for ingestion and querying
func New(ctx context.Context) (*RAG, error) {
	r := newRedisClient()
	embed := newEmbedder(embeddingModel)

	if err := r.Heartbeat(ctx); err != nil {
		return nil, fmt.Errorf("redis unreachable: %w", err)
	}

	return &RAG{redis: r, embedder: embed}, nil
}

// IngestDocument reads a markdown file, chunks + embeds it, and upserts into Redis.
// Safe to call multiple times — existing chunks are updated, not duplicated.
func (r *RAG) IngestDocument(ctx context.Context, collectionName string, filePath string) (int, error) {
	fmt.Printf(" -> Reading document: %s\n", filePath)

	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("cannot open %s: %w", filePath, err)
	}
	defer f.Close()

	raw, err := io.ReadAll(f)
	if err != nil {
		return 0, fmt.Errorf("cannot read %s: %w", filePath, err)
	}

	// Chunk the markdown document by headers
	cleanRaw := sanitizeForEmbedding(string(raw))
	chunks := ChunkDocument(filePath, cleanRaw)
	fmt.Printf(" -> Created %d chunks from %s\n", len(chunks), filePath)

	if len(chunks) == 0 {
		return 0, fmt.Errorf("no chunks produced from document — check file format")
	}

	// Generate embeddings for all chunks
	fmt.Printf(" -> Generating embeddings with %s...\n", embeddingModel)
	embedded, err := r.embedder.EmbedChunks(ctx, chunks)
	if err != nil {
		return 0, fmt.Errorf("embedding failed: %w", err)
	}

	dim := len(embedded[0].Embedding)
	if err := r.redis.GetOrCreateIndex(ctx, collectionName, dim); err != nil {
		return 0, fmt.Errorf("redis collection setup failed: %w", err)
	}
	fmt.Printf(" -> Redis Index: %s (dims: %d)\n", collectionName, dim)

	// Upsert all chunks into Redis
	if err := r.redis.UpsertChunks(ctx, collectionName, embedded); err != nil {
		return 0, fmt.Errorf("upsert failed: %w", err)
	}

	fmt.Printf(" ✅ Ingested %d chunks into Redis\n", len(embedded))
	return len(embedded), nil
}

// IngestJobs takes scraped jobs, embeds their title+description, and stores them in the 'jobs' collection
func (r *RAG) IngestJobs(ctx context.Context, collectionName string, jobs []scraper.Job) (int, error) {
	if len(jobs) == 0 {
		return 0, nil
	}

	var chunks []Chunk
	for _, j := range jobs {
		content := fmt.Sprintf("Title: %s\nCompany: %s\nDescription: %s", j.Title, j.Company, j.Description)
		chunks = append(chunks, Chunk{
			ID:      j.ID,
			Header:  j.Title,
			Source:  j.URL,
			Content: sanitizeForEmbedding(content),
		})
	}

	embedded, err := r.embedder.EmbedChunks(ctx, chunks)
	if err != nil {
		return 0, fmt.Errorf("job embedding failed: %w", err)
	}
	if len(embedded) == 0 {
		return 0, fmt.Errorf("no jobs were successfully embedded; skipping index creation")
	}

	dim := len(embedded[0].Embedding)
	if err := r.redis.GetOrCreateIndex(ctx, collectionName, dim); err != nil {
		return 0, fmt.Errorf("jobs collection setup failed: %w", err)
	}

	if err := r.redis.UpsertChunks(ctx, collectionName, embedded); err != nil {
		return 0, fmt.Errorf("upsert jobs failed: %w", err)
	}

	fmt.Printf(" ✅ Ingested %d jobs into Redis vector index\n", len(embedded))
	return len(embedded), nil
}

// QueryRelevant returns the top-k most semantically relevant chunks for a given text query.
// Used to find which parts of project_history.md best match a job description.
func (r *RAG) QueryRelevant(ctx context.Context, collectionName string, queryText string, topK int) ([]QueryResult, error) {
	// Embed the query text
	vec, err := r.embedder.EmbedText(ctx, queryText)
	if err != nil {
		return nil, fmt.Errorf("query embedding failed: %w", err)
	}

	// Retrieve top-k similar chunks from Redis
	results, err := r.redis.Query(ctx, collectionName, vec, topK)
	if err != nil {
		return nil, fmt.Errorf("redis query failed: %w", err)
	}
	return results, nil
}

func sanitizeForEmbedding(s string) string {
	// Strip control characters, weird emojis, and broken unicode that often crash local embedding models
	// This preserves standard ASCII (space to ~) plus newlines/tabs
	reg := regexp.MustCompile(`[^\x09\x0A\x0D\x20-\x7E]+`)
	return reg.ReplaceAllString(s, " ")
}

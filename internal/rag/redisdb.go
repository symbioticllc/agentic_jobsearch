package rag

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"

	"github.com/redis/go-redis/v9"
)

type redisClient struct {
	client *redis.Client
}

func newRedisClient() *redisClient {
	return &redisClient{
		client: redis.NewClient(&redis.Options{
			Addr: "localhost:6379", // Default redis-stack-server port
		}),
	}
}

func (r *redisClient) Heartbeat(ctx context.Context) error {
	return r.client.Ping(ctx).Err()
}

// GetOrCreateIndex creates a RediSearch Vector index if it doesn't already exist.
func (r *redisClient) GetOrCreateIndex(ctx context.Context, indexName string, dim int) error {
	prefix := indexName + ":"
	
	// Check if exists
	_, err := r.client.Do(ctx, "FT.INFO", indexName).Result()
	if err == nil {
		return nil // Index already exists
	}

	// Create index
	// FT.CREATE idx ON HASH PREFIX 1 prefix: SCHEMA id TEXT header TEXT content TEXT embedding VECTOR FLAT 6 TYPE FLOAT32 DIM {dim} DISTANCE_METRIC COSINE
	_, err = r.client.Do(ctx,
		"FT.CREATE", indexName,
		"ON", "HASH",
		"PREFIX", "1", prefix,
		"SCHEMA",
		"id", "TAG",
		"source", "TEXT",
		"header", "TEXT",
		"content", "TEXT",
		"embedding", "VECTOR", "FLAT", "6",
		"TYPE", "FLOAT32",
		"DIM", dim,
		"DISTANCE_METRIC", "COSINE",
	).Result()

	if err != nil && err.Error() != "Index already exists" {
		return fmt.Errorf("failed to create redis vector index: %w", err)
	}

	return nil
}

// float32ToBytes encodes a float32 slice to raw bytes for Redis vector insertion
func float32ToBytes(floats []float32) []byte {
	bytes := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(bytes[i*4:], math.Float32bits(f))
	}
	return bytes
}

// UpsertChunks inserts the documents using HSET
func (r *redisClient) UpsertChunks(ctx context.Context, indexName string, chunks []Chunk) error {
	prefix := indexName + ":"
	
	pipe := r.client.Pipeline()
	for _, ch := range chunks {
		key := prefix + ch.ID
		vecBytes := float32ToBytes(ch.Embedding)
		pipe.HSet(ctx, key, map[string]interface{}{
			"id":        ch.ID,
			"source":    ch.Source,
			"header":    ch.Header,
			"content":   ch.Content,
			"embedding": vecBytes,
		})
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed to pipeline HSET chunks: %w", err)
	}
	return nil
}

// Query performs a KNN Vector Search against the RediSearch index
func (r *redisClient) Query(ctx context.Context, indexName string, queryEmbedding []float32, topK int) ([]QueryResult, error) {
	vecBytes := float32ToBytes(queryEmbedding)
	
	// FT.SEARCH index "*=>[KNN topK @embedding $vec AS distance]" PARAMS 2 vec vecBytes DIALECT 2
	queryStr := fmt.Sprintf("*=>[KNN %d @embedding $vec AS distance]", topK)
	
	res, err := r.client.Do(ctx,
		"FT.SEARCH", indexName, queryStr,
		"PARAMS", "2", "vec", vecBytes,
		"RETURN", "5", "id", "source", "header", "content", "distance",
		"SORTBY", "distance", "ASC",
		"DIALECT", "2",
	).Result()

	if err != nil {
		return nil, fmt.Errorf("redis ft.search failed: %w", err)
	}

	// In DIALECT 2, go-redis v9 returns map[interface{}]interface{}
	resMap, ok := res.(map[interface{}]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected ft.search output format (expected map)")
	}

	resultsRaw, ok := resMap["results"].([]interface{})
	if !ok {
		return nil, nil // no results found
	}

	var results []QueryResult
	for _, rawRes := range resultsRaw {
		resObj, ok := rawRes.(map[interface{}]interface{})
		if !ok {
			continue
		}

		extraRaw, ok := resObj["extra_attributes"]
		if !ok {
			continue
		}

		attrs, ok := extraRaw.(map[interface{}]interface{})
		if !ok {
			continue
		}

		ch := Chunk{}
		var distance float64

		for k, v := range attrs {
			kStr, ok := k.(string)
			if !ok {
				continue
			}
			vStr, ok := v.(string)
			if !ok {
				continue
			}

			switch kStr {
			case "id":
				ch.ID = vStr
			case "source":
				ch.Source = vStr
			case "header":
				ch.Header = vStr
			case "content":
				ch.Content = vStr
			case "distance":
				if vStr == "nan" {
					distance = 0.0
				} else {
					fmt.Sscanf(vStr, "%f", &distance)
				}
			}
		}

		results = append(results, QueryResult{
			Chunk:    ch,
			Distance: distance,
		})
	}

	return results, nil
}

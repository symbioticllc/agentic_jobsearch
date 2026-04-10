package rag

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

const (
	maxChunkBytes = 2000 // soft cap on chunk size — keeps embeddings focused
	minChunkBytes = 50   // skip trivially short sections
)

// ChunkDocument splits a markdown document into meaningful sections by headers.
// Each top-level (##) and second-level (###) section becomes its own Chunk.
func ChunkDocument(source, content string) []Chunk {
	var chunks []Chunk
	var currentHeader string
	var currentLines []string

	flush := func() {
		text := strings.TrimSpace(strings.Join(currentLines, "\n"))
		if len(text) < minChunkBytes {
			return
		}
		// If chunk is too large, split on double-newlines (paragraphs)
		if len(text) > maxChunkBytes {
			subChunks := splitLargeChunk(source, currentHeader, text)
			chunks = append(chunks, subChunks...)
			return
		}
		chunks = append(chunks, newChunk(source, currentHeader, text))
	}

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "### ") {
			flush()
			currentHeader = strings.TrimLeft(trimmed, "# ")
			currentLines = []string{}
			continue
		}
		currentLines = append(currentLines, line)
	}
	flush() // capture the last section

	return chunks
}

// splitLargeChunk breaks an oversized section into paragraph-sized sub-chunks
func splitLargeChunk(source, header, text string) []Chunk {
	paragraphs := strings.Split(text, "\n\n")
	var chunks []Chunk
	var buf strings.Builder

	for _, para := range paragraphs {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		if buf.Len()+len(para) > maxChunkBytes && buf.Len() > 0 {
			chunks = append(chunks, newChunk(source, header, buf.String()))
			buf.Reset()
		}
		if buf.Len() > 0 {
			buf.WriteString("\n\n")
		}
		buf.WriteString(para)
	}
	if buf.Len() >= minChunkBytes {
		chunks = append(chunks, newChunk(source, header, buf.String()))
	}
	return chunks
}

// newChunk builds a Chunk with a deterministic ID based on content hash
func newChunk(source, header, content string) Chunk {
	hash := sha256.Sum256([]byte(source + header + content))
	id := fmt.Sprintf("%x", hash[:8])
	return Chunk{
		ID:        id,
		Source:    source,
		Header:    header,
		Content:   content,
		CreatedAt: time.Now(),
	}
}

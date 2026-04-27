package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/leee/agentic-jobs/internal/aligner"
	"github.com/leee/agentic-jobs/internal/llm"
	"github.com/leee/agentic-jobs/internal/rag"
	"github.com/leee/agentic-jobs/internal/store"
)

func main() {
	if err := llm.InitLLM(); err != nil {
		log.Fatalf("llm init error: %v", err)
	}

	db, err := store.NewSQLiteStore("./data/jobs.db")
	if err != nil {
		log.Fatalf("Store init error: %v", err)
	}
	defer db.Close()

	savedFast, _ := db.GetSetting("system", "llm_fast_model")
	savedDeep, _ := db.GetSetting("system", "llm_deep_model")
	if savedFast != "" && savedDeep != "" {
		llm.ConfigureModels(savedFast, savedDeep)
	} else {
		llm.PreloadModels()
	}

	ragPipeline, _ := rag.New(context.Background())
	alg := aligner.NewAligner(ragPipeline)

	userID := "info@symbiotic.us"
	job, err := db.GetJobByID(userID, "gh-5024524007")
	if err != nil {
		log.Fatalf("Job not found: %v", err)
	}

	templatePath := filepath.Join("./experience", userID, "base_resume.md")
	baseResumeRaw, err := os.ReadFile(templatePath)
	if err != nil {
		baseResumeRaw, err = os.ReadFile("./base_resume.md")
	}
	if err != nil {
		baseResumeRaw, err = os.ReadFile("./project_history.md")
	}
	if err != nil {
		log.Fatalf("no resume found: %v", err)
	}

	linkedInUrl, _ := db.GetSetting(userID, "linkedin_url")

	fmt.Printf("Starting tailoring for %s\n", job.Title)
	result, err := alg.TailorResume(context.Background(), job, string(baseResumeRaw), linkedInUrl, "")
	if err != nil {
		log.Fatalf("TailorResume failed: %v", err)
	}

	fmt.Printf("Success! Score: %d\n", result.Score)
}

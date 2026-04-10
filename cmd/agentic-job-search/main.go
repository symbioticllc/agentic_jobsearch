package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leee/agentic-jobs/internal/aligner"
	"github.com/leee/agentic-jobs/internal/drive"
	"github.com/leee/agentic-jobs/internal/llm"
	"github.com/leee/agentic-jobs/internal/rag"
	"github.com/leee/agentic-jobs/internal/scraper"
	"github.com/leee/agentic-jobs/internal/store"
)

const (
	projectHistoryPath = "./project_history.md"
	port               = ":8081"
)

type server struct {
	db           *store.SQLiteStore
	scraper      *scraper.Manager
	aligner      *aligner.Aligner
	ragPipeline  *rag.RAG
	scrapeCancel context.CancelFunc
	scrapeMutex  sync.Mutex
}

func main() {
	fmt.Println("🚀 Initializing Agentic Job Search Architecture as Web Server...")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// ── Step 1: Health Checks & Init ─────────────────────────────────────────
	if err := rag.InitRedis(); err != nil {
		log.Printf("RAG health check warning: %v\n", err)
	}

	ragPipeline, err := rag.New(ctx)
	if err != nil {
		log.Printf("RAG init failed: %v\n", err)
	}

	manager, err := scraper.InitScraper()
	if err != nil {
		log.Fatalf("Scraper init error: %v\n", err)
	}

	if err := llm.InitLLM(); err != nil {
		log.Printf("LLM init error: %v\n", err)
	}

	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	db, err := store.NewSQLiteStore("./data/jobs.db")
	if err != nil {
		log.Fatalf("Store init error: %v\n", err)
	}
	defer db.Close()

	// Ingest base context
	if ragPipeline != nil {
		if _, err := os.Stat(projectHistoryPath); err == nil {
			fmt.Println("📚 Ingesting project history into RAG...")
			_, err := ragPipeline.IngestDocument(ctx, "project_history", projectHistoryPath)
			if err != nil {
				log.Printf("⚠️ Project history ingest error: %v", err)
			}
		}

		// Sync existing SQLite database jobs into Redis vector index in the background
		go func() {
			fmt.Println("📚 Synchronizing SQLite jobs into Redis DB (Background)...")
			existingJobs, err := db.GetAllJobs()
			if err == nil && len(existingJobs) > 0 {
				_, err := ragPipeline.IngestJobs(context.Background(), "jobs", existingJobs)
				if err != nil {
					log.Printf("⚠️ Failed to sync historical jobs into vector DB: %v\n", err)
				} else {
					fmt.Printf("✅ Synchronized %d historically scraped jobs into Redis\n", len(existingJobs))
				}
			}
		}()
	}

	srv := &server{
		db:          db,
		scraper:     manager,
		ragPipeline: ragPipeline,
		aligner:     aligner.NewAligner(ragPipeline),
	}

	// ── Step 2: Setup HTTP Routes ────────────────────────────────────────────
	mux := http.NewServeMux()

	// Static UI Server
	fs := http.FileServer(http.Dir("./ui"))
	mux.Handle("/", fs)

	// API Routes
	mux.HandleFunc("GET /api/jobs", srv.handleGetJobs)
	mux.HandleFunc("POST /api/scrape", srv.handleScrapeJobs)
	mux.HandleFunc("POST /api/scrape/stop", srv.handleStopScrape)
	mux.HandleFunc("POST /api/jobs/tailor/{id}", srv.handleTailorJob)
	mux.HandleFunc("POST /api/export/gdocs/{id}", srv.handleGoogleDocsExport)
	mux.HandleFunc("GET /api/jobs/search", srv.handleSearchJobs)
	mux.HandleFunc("GET /api/resumes", srv.handleListResumes)

	fmt.Printf("\n✅ Server online! Available at http://localhost%s\n", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// GET /api/jobs
func (s *server) handleGetJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.db.GetAllJobs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jobs)
}

// POST /api/scrape/stop
func (s *server) handleStopScrape(w http.ResponseWriter, r *http.Request) {
	s.scrapeMutex.Lock()
	defer s.scrapeMutex.Unlock()
	
	if s.scrapeCancel != nil {
		s.scrapeCancel()
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "aborted"})
		return
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "not_running"})
}

// POST /api/scrape
func (s *server) handleScrapeJobs(w http.ResponseWriter, r *http.Request) {
	s.scrapeMutex.Lock()
	if s.scrapeCancel != nil {
		s.scrapeMutex.Unlock()
		http.Error(w, "A scrape is already actively running", http.StatusConflict)
		return
	}
	
	ctx, cancel := context.WithCancel(context.Background())
	s.scrapeCancel = cancel
	s.scrapeMutex.Unlock()

	defer func() {
		s.scrapeMutex.Lock()
		s.scrapeCancel = nil
		s.scrapeMutex.Unlock()
		cancel()
	}()

	targetCompanies := loadTargetCompanies("./target_companies")

	keywords := []string{"golang", "go", "backend", "architect", "staff", "principal"}
	if r.URL.Query().Get("exec") == "true" {
		keywords = append(keywords, "director", "head of", "vp", "vice president", "chief", "cto", "cxo", "ciso", "cio")
		fmt.Println("🚀 Executive Scrape Mode Enabled: Casting broader net for leadership roles.")
	}

	query := scraper.SearchQuery{
		Keywords:        keywords,
		Remote:          true,
		TargetCompanies: targetCompanies,
	}
	jobs, err := s.scraper.ScrapeAll(ctx, query)
	if err != nil {
		if ctx.Err() == context.Canceled {
			http.Error(w, "Scraping was intentionally aborted by the user", http.StatusRequestTimeout)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	added, err := s.db.SaveJobs(jobs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	// Background ingest into redis
	go func() {
		_, err := s.ragPipeline.IngestJobs(context.Background(), "jobs", jobs)
		if err != nil {
			log.Printf("⚠️ Background Redis ingestion error: %v", err)
		}
	}()
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]int{"scraped": len(jobs), "added": added})
}

// GET /api/resumes
func (s *server) handleListResumes(w http.ResponseWriter, r *http.Request) {
	var resumes []string
	entries, err := os.ReadDir("./experience")
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				resumes = append(resumes, e.Name())
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resumes)
}

// POST /api/jobs/tailor/{id}
func (s *server) handleTailorJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing job id", http.StatusBadRequest)
		return
	}

	job, err := s.db.GetJobByID(id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	templateName := r.URL.Query().Get("template")
	if templateName == "" {
		templateName = "base_resume.md"
	}
	
	// Directory traversal protection implicitly via filepath.Join + specific dir checking
	templatePath := filepath.Join("./experience", templateName)
	baseResumeRaw, err := os.ReadFile(templatePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not read base resume template '%s'", templateName), http.StatusInternalServerError)
		return
	}

	if s.ragPipeline == nil {
		http.Error(w, "RAG pipeline not initialized", http.StatusInternalServerError)
		return
	}

	fmt.Printf("📝 Tailoring resume for %s @ %s using %s\n", job.Title, job.Company, templateName)
	ctx := context.Background()
	result, err := s.aligner.TailorResume(ctx, job, string(baseResumeRaw))
	if err != nil {
		http.Error(w, fmt.Sprintf("tailor error: %v", err), http.StatusInternalServerError)
		return
	}

	if err := aligner.SaveJobProfile(job, result); err != nil {
		log.Printf("Failed to save job profile: %v\n", err)
	}

	if err := aligner.SaveTailoredResume(job, result); err != nil {
		log.Printf("Failed to save tailored resume: %v\n", err)
	}

	// Persist generated alignment securely to the central jobs database
	if err := s.db.SaveTailoredResult(job.ID, result.TailoredResume, result.Report, result.FitBrief, result.MarketSalary, result.Score); err != nil {
		log.Printf("⚠️ Failed to explicitly map Tailoring data back to the database: %v\n", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// POST /api/export/gdocs/{id}
func (s *server) handleGoogleDocsExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing job id", http.StatusBadRequest)
		return
	}

	job, err := s.db.GetJobByID(id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	if job.TailoredResume == "" {
		http.Error(w, "job has not been tailored. cannot export.", http.StatusBadRequest)
		return
	}

	var reqBody struct {
		RawHTML string `json:"rawHtml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	safeCompany := strings.ReplaceAll(job.Company, " ", "_")
	title := fmt.Sprintf("%s-Mike_Lee_%s", time.Now().Format("20060102"), safeCompany)
	url, err := drive.UploadAsDocx(r.Context(), title, reqBody.RawHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"url": url})
}

// loadTargetCompanies reads all txt files in the given directory and returns a unified list of company names
func loadTargetCompanies(dir string) []string {
	var companies []string
	
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("Warning: failed to read target_companies directory: %v", err)
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".txt") {
			path := filepath.Join(dir, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line != "" {
					companies = append(companies, line)
				}
			}
		}
	}
	fmt.Printf("Loaded %d target companies from %s\n", len(companies), dir)
	return companies
}

// GET /api/jobs/search?q=
func (s *server) handleSearchJobs(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "missing q parameter", http.StatusBadRequest)
		return
	}

	type JobSearchResult struct {
		scraper.Job
		VectorDistance float64 `json:"vector_distance"`
		ExactMatch     bool    `json:"exact_match"`
	}

	matchedJobsMap := make(map[string]JobSearchResult)
	
	// 1. Perform SQLite FTS5 Search for exact keywords (Instant, Highly Reliable)
	ftsJobs, err := s.db.SearchFTS(query)
	if err == nil {
		log.Printf("🔍 FTS Search for %q retuned %d exact SQLite matches", query, len(ftsJobs))
		for _, j := range ftsJobs {
			matchedJobsMap[j.ID] = JobSearchResult{
				Job:            j,
				VectorDistance: 0.0, // perfect
				ExactMatch:     true,
			}
		}
	} else {
		log.Printf("⚠️ SQLite FTS search error: %v", err)
	}

	// 2. Perform Redis Vector Semantic Search (Conceptual matching)
	results, err := s.ragPipeline.QueryRelevant(r.Context(), "jobs", query, 20)
	if err != nil {
		log.Printf("⚠️ Vector search error for query %q: %v", query, err)
	} else {
		log.Printf("🔍 Semantic Search for %q retuned %d Redis chunks", query, len(results))
		for _, res := range results {
			if res.Distance > 1.2 {
				continue // skip terrible matches
			}
			if _, exists := matchedJobsMap[res.Chunk.ID]; exists {
				continue // already secured a spot via exact FTS keyword!
			}
			
			job, err := s.db.GetJobByID(res.Chunk.ID)
			if err == nil {
				matchedJobsMap[res.Chunk.ID] = JobSearchResult{
					Job:            job,
					VectorDistance: res.Distance,
					ExactMatch:     false,
				}
			}
		}
	}

	// Flatten map to slice
	var matchedJobs []JobSearchResult
	for _, v := range matchedJobsMap {
		matchedJobs = append(matchedJobs, v)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(matchedJobs)
}

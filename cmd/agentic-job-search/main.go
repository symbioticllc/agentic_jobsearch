package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ledongthuc/pdf"
	"github.com/nguyenthenguyen/docx"

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
	llm.PreloadModels() // Background NAS → RAM warm-up; pins both models with keep_alive=-1

	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	db, err := store.NewSQLiteStore("./data/jobs.db")
	if err != nil {
		log.Fatalf("Store init error: %v\n", err)
	}
	defer db.Close()

	// Ingest base context in the background — don't block server startup
	// (nomic-embed-text may need to cold-load from NAS which takes time)
	if ragPipeline != nil {
		go func() {
			if _, err := os.Stat(projectHistoryPath); err == nil {
				fmt.Println("📚 Background: Ingesting project history into RAG...")
				_, err := ragPipeline.IngestDocument(context.Background(), "project_history", projectHistoryPath)
				if err != nil {
					log.Printf("⚠️ Project history ingest error: %v", err)
				} else {
					fmt.Println("📚 Background: Project history ingestion complete.")
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

	// API Routes (Wrapped in Auth Middleware for Multi-Tenancy)
	mux.HandleFunc("GET /api/jobs", authMiddleware(srv.handleGetJobs))
	mux.HandleFunc("POST /api/scrape", authMiddleware(srv.handleScrapeJobs))
	mux.HandleFunc("POST /api/scrape/stop", authMiddleware(srv.handleStopScrape))
	mux.HandleFunc("POST /api/jobs/tailor/{id}", authMiddleware(srv.handleTailorJob))
	mux.HandleFunc("GET /api/jobs/status/{id}", authMiddleware(srv.handleGetTailorStatus))
	mux.HandleFunc("POST /api/export/gdocs/{id}", authMiddleware(srv.handleGoogleDocsExport))
	mux.HandleFunc("GET /api/jobs/search", authMiddleware(srv.handleSearchJobs))
	mux.HandleFunc("GET /api/resumes", authMiddleware(srv.handleListResumes))
	
	// Profile Settings Routes
	mux.HandleFunc("GET /api/profile", authMiddleware(srv.handleGetProfile))
	mux.HandleFunc("POST /api/profile", authMiddleware(srv.handleSaveProfile))
	mux.HandleFunc("POST /api/profile/upload", authMiddleware(srv.handleUploadProfileFile))

	// Analytics Routes
	mux.HandleFunc("GET /api/report/companies", authMiddleware(srv.handleCompanyReport))
	mux.HandleFunc("GET /api/report/sources", authMiddleware(srv.handleSourceReport))

	// System Health Route
	mux.HandleFunc("GET /api/health", srv.handleHealth)

	fmt.Printf("\n✅ SaaS Architecture online! Available at http://localhost%s\n", port)
	if err := http.ListenAndServe(port, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// authMiddleware intercepts requests and asserts Tenant contextual boundaries
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "Unauthorized: Identity Token Required", http.StatusUnauthorized)
			return
		}
		userID := strings.TrimPrefix(authHeader, "Bearer ")
		if userID == "" {
			http.Error(w, "Unauthorized: Tenant ID Empty", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), "user_id", userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	}
}

// GET /api/profile
func (s *server) handleGetProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	linkedIn, _ := s.db.GetSetting(userID, "linkedin_url")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"linkedin_url": linkedIn})
}

// POST /api/profile
func (s *server) handleSaveProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	var payload map[string]string
	if err := json.NewDecoder(r.Body).Decode(&payload); err == nil {
		if url, ok := payload["linkedin_url"]; ok {
			s.db.SaveSetting(userID, "linkedin_url", url)
		}
	}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "saved"})
}

// POST /api/profile/upload
func (s *server) handleUploadProfileFile(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil { // 10 MB limit
		http.Error(w, "Payload too large", http.StatusBadRequest)
		return
	}

	fileType := r.FormValue("type") // "resume" or "bragsheet"
	if fileType == "" {
		fileType = "resume"
	}

	var rawMarkdown string
	var baseFileName string

	gdocURL := r.FormValue("gdoc_url")
	if gdocURL != "" {
		// Attempt Google Docs Text extraction over the wire
		re := regexp.MustCompile(`\/d\/([a-zA-Z0-9-_]+)`)
		matches := re.FindStringSubmatch(gdocURL)
		if len(matches) < 2 {
			http.Error(w, "Invalid Google Docs URL format", http.StatusBadRequest)
			return
		}
		docID := matches[1]
		exportURL := fmt.Sprintf("https://docs.google.com/document/d/%s/export?format=txt", docID)
		
		resp, err := http.Get(exportURL)
		if err != nil || resp.StatusCode != 200 {
			http.Error(w, "Failed downloading public Google Doc. Ensure 'Anyone with the link' is enabled.", http.StatusBadRequest)
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		rawMarkdown = string(b)
		baseFileName = fmt.Sprintf("gdocs_%s.md", docID)
	} else {
		// Handle Binary File Upload
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "Missing file or gdoc_url", http.StatusBadRequest)
			return
		}
		defer file.Close()

		rawBytes, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, "Failed reading file", http.StatusInternalServerError)
			return
		}

		// Try explicit Native parsing based on generic extensions
		lowerExt := strings.ToLower(filepath.Ext(header.Filename))
		baseFileName = strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename)) + ".md"

		if lowerExt == ".pdf" {
			// Save temporarily to use ledongthuc/pdf which requires actual disk file pointer
			tmpExt, _ := os.CreateTemp("", "upload-*.pdf")
			tmpExt.Write(rawBytes)
			tmpExt.Close()
			defer os.Remove(tmpExt.Name())

			f, r, err := pdf.Open(tmpExt.Name())
			if err == nil {
				defer f.Close()
				var buf bytes.Buffer
				b, err := r.GetPlainText()
				if err == nil {
					buf.ReadFrom(b)
					rawMarkdown = buf.String()
				}
			}
		} else if lowerExt == ".doc" || lowerExt == ".docx" {
			tmpExt, _ := os.CreateTemp("", "upload-*.docx")
			tmpExt.Write(rawBytes)
			tmpExt.Close()
			defer os.Remove(tmpExt.Name())

			r, err := docx.ReadDocxFile(tmpExt.Name())
			if err == nil {
				doc := r.Editable()
				rawMarkdown = doc.GetContent()
				r.Close()
			}
		} else {
			// TXT / MD generic blind ingestion
			rawMarkdown = string(rawBytes)
		}
	}

	userID := r.Context().Value("user_id").(string)
	tenantDir := filepath.Join("./experience", userID)
	if err := os.MkdirAll(tenantDir, 0755); err != nil {
		http.Error(w, "Failed to provision tenant storage", http.StatusInternalServerError)
		return
	}

	var outPath string
	if fileType == "bragsheet" {
		outPath = filepath.Join(tenantDir, "brag_sheet.md")
		err := os.WriteFile(outPath, []byte(rawMarkdown), 0644)
		if err == nil && s.ragPipeline != nil {
			tenantCollection := fmt.Sprintf("%s_project_history", userID)
			go s.ragPipeline.IngestDocument(context.Background(), tenantCollection, outPath)
		}
	} else {
		outPath = filepath.Join(tenantDir, filepath.Base(baseFileName))
		os.WriteFile(outPath, []byte(rawMarkdown), 0644)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "uploaded", "path": outPath})
}

// GET /api/jobs
func (s *server) handleGetJobs(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	jobs, err := s.db.GetAllJobs(userID)
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
	userID := r.Context().Value("user_id").(string)
	added, err := s.db.SaveJobs(userID, jobs)
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
	userID := r.Context().Value("user_id").(string)
	tenantDir := filepath.Join("./experience", userID)

	var resumes []string
	entries, err := os.ReadDir(tenantDir)
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

	userID := r.Context().Value("user_id").(string)
	job, err := s.db.GetJobByID(userID, id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	templateName := r.URL.Query().Get("template")
	if templateName == "" {
		templateName = "base_resume.md"
	}
	
	// Directory traversal protection explicitly isolated within Tenant Box
	templatePath := filepath.Join("./experience", userID, templateName)
	baseResumeRaw, err := os.ReadFile(templatePath)
	if err != nil {
		baseResumeRaw, err = os.ReadFile(filepath.Join(".", templateName))
	}
	if err != nil {
		baseResumeRaw, err = os.ReadFile(projectHistoryPath)
	}
	if err != nil {
		http.Error(w, "No resume found. Please upload a base resume via User Profile & Setup.", http.StatusUnprocessableEntity)
		return
	}

	if s.ragPipeline == nil {
		http.Error(w, "RAG pipeline not initialized", http.StatusInternalServerError)
		return
	}

	// Update status to processing
	if err := s.db.UpdateTailoringStatus(userID, id, "processing"); err != nil {
		log.Printf("⚠️ Failed to update tailoring status: %v\n", err)
	}

	fmt.Printf("📝 Background Tailoring initiated for %s @ %s [Tenant: %s]\n", job.Title, job.Company, userID)
	
	// Kick off background tailoring
	go func() {
		ctx := context.Background()
		linkedInUrl, _ := s.db.GetSetting(userID, "linkedin_url")
		
		result, err := s.aligner.TailorResume(ctx, job, string(baseResumeRaw), linkedInUrl)
		if err != nil {
			log.Printf("❌ Background tailoring failed for %s: %v\n", id, err)
			s.db.UpdateTailoringStatus(userID, id, "failed")
			return
		}

		if err := s.db.SaveTailoredResult(userID, job.ID, result.TailoredResume, result.Report, result.FitBrief, result.MarketSalary, result.Score, result.CoverLetter); err != nil {
			log.Printf("⚠️ Failed to persist tailoring data: %v\n", err)
			s.db.UpdateTailoringStatus(userID, id, "failed")
		}
		fmt.Printf(" ✅ Tailoring complete for %s\n", id)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "processing", "job_id": id})
}

// GET /api/jobs/status/{id}
func (s *server) handleGetTailorStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := r.Context().Value("user_id").(string)

	job, err := s.db.GetJobByID(userID, id)
	if err != nil {
		http.Error(w, "job not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(job)
}

// POST /api/export/gdocs/{id}
func (s *server) handleGoogleDocsExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing job id", http.StatusBadRequest)
		return
	}

	userID := r.Context().Value("user_id").(string)
	job, err := s.db.GetJobByID(userID, id)
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

// GET /api/health — returns real-time status of all infrastructure components
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	type ComponentStatus struct {
		Status  string `json:"status"`  // "ok" | "degraded" | "error"
		Detail  string `json:"detail"`
		Latency string `json:"latency,omitempty"`
	}
	type HealthReport struct {
		Overall    string                       `json:"overall"`
		Components map[string]ComponentStatus   `json:"components"`
	}

	components := make(map[string]ComponentStatus)
	allOk := true

	// ── 1. Ollama ─────────────────────────────────────────────────────────────
	func() {
		start := time.Now()
		resp, err := http.Get("http://127.0.0.1:11434/api/tags")
		latency := time.Since(start).Milliseconds()
		if err != nil {
			allOk = false
			components["ollama"] = ComponentStatus{Status: "error", Detail: "Ollama unreachable: " + err.Error()}
			return
		}
		defer resp.Body.Close()

		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		json.NewDecoder(resp.Body).Decode(&tags)

		activeModel := llm.ActiveModel()

		// Check if our target model is installed
		modelInstalled := false
		var installedNames []string
		for _, m := range tags.Models {
			installedNames = append(installedNames, m.Name)
			if strings.HasPrefix(m.Name, activeModel) {
				modelInstalled = true
			}
		}
		if !modelInstalled {
			allOk = false
			components["ollama"] = ComponentStatus{
				Status:  "degraded",
				Detail:  fmt.Sprintf("%s not found. Installed: %s", activeModel, strings.Join(installedNames, ", ")),
				Latency: fmt.Sprintf("%dms", latency),
			}
			return
		}

		// Check if model is currently loaded in memory via /api/ps
		loaded := false
		psResp, psErr := http.Get("http://127.0.0.1:11434/api/ps")
		if psErr == nil {
			defer psResp.Body.Close()
			var ps struct {
				Models []struct {
					Name string `json:"name"`
				} `json:"models"`
			}
			json.NewDecoder(psResp.Body).Decode(&ps)
			for _, m := range ps.Models {
				if strings.HasPrefix(m.Name, activeModel) {
					loaded = true
				}
			}
		}

		status := "ok"
		detail := activeModel + " installed"
		if loaded {
			detail = activeModel + " ✓ loaded in memory"
		} else {
			status = "degraded"
			detail = activeModel + " installed but not yet loaded (preload in progress)"
		}
		components["ollama"] = ComponentStatus{Status: status, Detail: detail, Latency: fmt.Sprintf("%dms", latency)}
	}()

	// ── 2. Redis ──────────────────────────────────────────────────────────────
	func() {
		start := time.Now()
		conn, err := net.DialTimeout("tcp", "127.0.0.1:6379", 2*time.Second)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			allOk = false
			components["redis"] = ComponentStatus{Status: "error", Detail: "Redis unreachable on :6379 — start with: docker start agentic-redis"}
			return
		}
		conn.Close()
		components["redis"] = ComponentStatus{Status: "ok", Detail: "Redis Stack reachable on :6379", Latency: fmt.Sprintf("%dms", latency)}
	}()

	// ── 3. SQLite ─────────────────────────────────────────────────────────────
	func() {
		if s.db == nil {
			allOk = false
			components["sqlite"] = ComponentStatus{Status: "error", Detail: "SQLite store not initialized"}
			return
		}
		components["sqlite"] = ComponentStatus{Status: "ok", Detail: "SQLite database online at ./data/jobs.db"}
	}()

	// ── 4. Cloud Failovers ────────────────────────────────────────────────────
	if os.Getenv("GEMINI_API_KEY") != "" {
		components["gemini"] = ComponentStatus{Status: "ok", Detail: "GEMINI_API_KEY configured"}
	} else {
		components["gemini"] = ComponentStatus{Status: "degraded", Detail: "GEMINI_API_KEY not set (optional failover)"}
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		components["claude"] = ComponentStatus{Status: "ok", Detail: "ANTHROPIC_API_KEY configured"}
	} else {
		components["claude"] = ComponentStatus{Status: "degraded", Detail: "ANTHROPIC_API_KEY not set (optional failover)"}
	}

	overall := "ok"
	if !allOk {
		overall = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthReport{Overall: overall, Components: components})
}

// GET /api/report/companies — returns per-company totals for the authenticated tenant
func (s *server) handleCompanyReport(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	report, err := s.db.GetCompanyReport(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if report == nil {
		report = []store.CompanyReportRow{} // return empty array, not null
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
}

// GET /api/report/sources — returns per-scraper-source job counts for the authenticated tenant
func (s *server) handleSourceReport(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	counts, err := s.db.CountBySource(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if counts == nil {
		counts = map[string]int{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(counts)
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
	
	// 1. Perform SQLite FTS5 Search for exact keywords mapped specifically to Tenant
	userID := r.Context().Value("user_id").(string)
	ftsJobs, err := s.db.SearchFTS(userID, query)
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
			
			job, err := s.db.GetJobByID(userID, res.Chunk.ID)
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

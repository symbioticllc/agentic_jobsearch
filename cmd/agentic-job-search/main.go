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
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
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

// scrapeProgress holds live telemetry about an in-progress scrape operation.
type scrapeProgress struct {
	Running       bool      `json:"running"`
	StartTime     time.Time `json:"-"`
	ElapsedSecs   int       `json:"elapsed_seconds"`
	JobsFound     int       `json:"jobs_found"`
	SourcesTotal  int       `json:"sources_total"`
	SourcesDone   int       `json:"sources_done"`
	SourceStatus  map[string]string `json:"source_statuses"` // name → "running"|"done"|"error"
}

type server struct {
	db           *store.SQLiteStore
	scraper      *scraper.Manager
	aligner      *aligner.Aligner
	ragPipeline  *rag.RAG
	scrapeCancel context.CancelFunc
	scrapeMutex  sync.Mutex
	scrapeProg   scrapeProgress
	scrapeProgMu sync.RWMutex
}

// ensureServicesRunning starts Ollama and Redis if they are not already reachable.
// Each service is probed first; if the probe fails the process is launched and we
// wait (up to 30 s) for it to become ready before continuing startup.
func ensureServicesRunning() {
	// ── Ollama (port 11434) ────────────────────────────────────────────────────
	// Ollama is started by ./start.sh before the Go binary runs.
	// We only probe here so we can emit a clear warning if it's still not up.
	if err := tcpProbe("127.0.0.1:11434"); err != nil {
		log.Printf("⚠️  Ollama is not reachable on port 11434 — LLM features will be unavailable. Run ./start.sh to auto-start it.")
	} else {
		fmt.Println("✅ Ollama already running.")
	}

	// ── Redis / Redis-Stack (port 6379) ────────────────────────────────────────
	if err := tcpProbe("127.0.0.1:6379"); err != nil {
		fmt.Println("⚡ Redis not detected — starting redis-stack-server...")
		redisCmd := "redis-stack-server"
		if _, lookErr := exec.LookPath(redisCmd); lookErr != nil {
			log.Fatalf("❌ FATAL: 'redis-stack-server' not found in PATH.\n\nAgentic Jobs requires Redis Stack (not standard Redis) for its vector database (RediSearch) features used in resume tailoring.\n\nTo install on macOS:\n  brew tap redis-stack/redis-stack\n  brew install redis-stack\n\nOr run via Docker:\n  docker run -d --name redis-stack -p 6379:6379 redis/redis-stack-server:latest\n")
		}
		cmd := exec.Command(redisCmd)
		cmd.Stdout = nil
		cmd.Stderr = nil
		if startErr := cmd.Start(); startErr != nil {
			log.Printf("⚠️  Could not start Redis Stack (%s): %v", redisCmd, startErr)
		} else {
			fmt.Printf(" -> Waiting for Redis (%s) to become ready...\n", redisCmd)
			waitForPort("127.0.0.1:6379", 30*time.Second)
		}
	} else {
		fmt.Println("✅ Redis already running.")
	}
}

// tcpProbe attempts a single TCP dial to addr. Returns nil if the port accepts connections.
func tcpProbe(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// waitForPort polls addr once per second until it is reachable or deadline passes.
func waitForPort(addr string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if tcpProbe(addr) == nil {
			fmt.Printf(" ✅ %s is ready.\n", addr)
			return
		}
		time.Sleep(1 * time.Second)
	}
	log.Printf("⚠️  Timed out waiting for %s — continuing anyway", addr)
}

func main() {
	fmt.Println("🚀 Initializing Agentic Job Search Architecture as Web Server...")

	// ── Step 0: Ensure Ollama & Redis are up ─────────────────────────────────
	ensureServicesRunning()

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

	// ── Step 2b: Restore saved model selection from DB (after DB is ready) ────
	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}
	db, err := store.NewSQLiteStore("./data/jobs.db")
	if err != nil {
		log.Fatalf("Store init error: %v\n", err)
	}
	defer db.Close()

	// Load user-configured model names from SQLite (fall back to defaults if not set)
	savedFast, _ := db.GetSetting("system", "llm_fast_model")
	savedDeep, _ := db.GetSetting("system", "llm_deep_model")
	if savedFast != "" && savedDeep != "" {
		fmt.Printf(" -> Restoring saved models: fast=%s deep=%s\n", savedFast, savedDeep)
		if err := llm.ConfigureModels(savedFast, savedDeep); err != nil {
			log.Printf("⚠️ Failed to restore saved models, using defaults: %v", err)
			llm.PreloadModels()
		}
	} else {
		llm.PreloadModels() // Background NAS → RAM warm-up; pins both models with keep_alive=-1
	}

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
	mux.HandleFunc("GET /api/scrape/status", authMiddleware(srv.handleScrapeStatus))
	mux.HandleFunc("POST /api/vectorize", authMiddleware(srv.handleVectorizeJobs))
	mux.HandleFunc("POST /api/scrape/stop", authMiddleware(srv.handleStopScrape))
	mux.HandleFunc("POST /api/jobs/tailor/{id}", authMiddleware(srv.handleTailorJob))
	mux.HandleFunc("POST /api/jobs/apply/{id}", authMiddleware(srv.handleSetApplied))
	mux.HandleFunc("GET /api/jobs/status/{id}", authMiddleware(srv.handleGetTailorStatus))
	mux.HandleFunc("POST /api/export/gdocs/{id}", authMiddleware(srv.handleGoogleDocsExport))
	mux.HandleFunc("GET /api/jobs/search", authMiddleware(srv.handleSearchJobs))
	mux.HandleFunc("GET /api/resumes", authMiddleware(srv.handleListResumes))
	mux.HandleFunc("DELETE /api/resumes/{id}", authMiddleware(srv.handleDeleteResume))
	
	// Profile Settings Routes
	mux.HandleFunc("GET /api/profile", authMiddleware(srv.handleGetProfile))
	mux.HandleFunc("POST /api/profile", authMiddleware(srv.handleSaveProfile))
	mux.HandleFunc("POST /api/profile/upload", authMiddleware(srv.handleUploadProfileFile))
	mux.HandleFunc("POST /api/profile/clear", authMiddleware(srv.handleClearTenantCache))

	// Analytics Routes
	mux.HandleFunc("GET /api/report/companies", authMiddleware(srv.handleCompanyReport))
	mux.HandleFunc("GET /api/report/sources", authMiddleware(srv.handleSourceReport))
	mux.HandleFunc("GET /api/report/trends", authMiddleware(srv.handleTrendReport))
	mux.HandleFunc("GET /api/jobs/history/{id}", authMiddleware(srv.handleTailoringHistory))

	// System Health + Model Config Routes
	mux.HandleFunc("GET /api/health", srv.handleHealth)
	mux.HandleFunc("GET /api/models", srv.handleListModels)
	mux.HandleFunc("POST /api/models/configure", authMiddleware(srv.handleConfigureModels))

	srvHTTP := &http.Server{
		Addr:    port,
		Handler: mux,
	}

	go func() {
		fmt.Printf("\n✅ SaaS Architecture online! Available at http://localhost%s\n", port)
		if err := srvHTTP.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	// Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	fmt.Println("\n🛑 Shutting down server gracefully...")

	// Signal all background tasks
	cancel()

	// Wait up to 10 seconds for active requests to finish
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srvHTTP.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Server forced to shutdown: %v", err)
	}

	fmt.Println("✅ Graceful shutdown complete")
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
func (s *server) handleClearTenantCache(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	
	// Delete jobs from SQLite
	if err := s.db.ClearTenantData(userID); err != nil {
		http.Error(w, "Failed to clear jobs", http.StatusInternalServerError)
		return
	}
	
	// Delete files from ./experience/userID
	tenantDir := filepath.Join(".", "experience", userID)
	os.RemoveAll(tenantDir) // deletes the directory and all contents
	os.MkdirAll(tenantDir, 0755) // recreate empty directory

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "cleared"})
}

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

	id, err := s.db.SaveUserResume(userID, filepath.Base(baseFileName), fileType, rawMarkdown)
	if err != nil {
		http.Error(w, "Failed to save file to database", http.StatusInternalServerError)
		return
	}

	if fileType == "bragsheet" && s.ragPipeline != nil {
		tenantCollection := fmt.Sprintf("%s_project_history", userID)
		go s.ragPipeline.IngestText(context.Background(), tenantCollection, baseFileName, rawMarkdown)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "uploaded", "id": id, "name": baseFileName})
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

	// We DO NOT defer the cancel and lock releasing here, 
	// because scraping happens in the background. 
	// We will handle it after the background task completes.

	targetCompanies := loadTargetCompanies("./target_companies")

	keywords := []string{
		"golang", "go", "backend", "architect", "staff", "principal",
		"distinguished engineer", "senior engineer", "software engineering",
		"director of engineering", "director of software", "engineering manager",
		"head of engineering", "platform engineer", "infrastructure",
		"vp engineering", "vp software", "director", "senior director",
	}
	if r.URL.Query().Get("exec") == "true" {
		keywords = append(keywords, "cto", "cio", "ciso", "cxo", "chief technology",
			"chief information", "chief architect", "vice president", "svp",
			"head of", "managing director", "executive", "fellow")
		fmt.Println("🚀 Executive Scrape Mode Enabled: Casting broader net for leadership roles.")
	}

	// Merge user-supplied custom keywords
	if custom := r.URL.Query().Get("keywords"); custom != "" {
		for _, kw := range strings.Split(custom, ",") {
			kw = strings.TrimSpace(kw)
			if kw != "" {
				keywords = append(keywords, kw)
			}
		}
		fmt.Printf("🎯 Custom keywords added: %s\n", custom)
	}

	query := scraper.SearchQuery{
		Keywords:        keywords,
		Remote:          true,
		TargetCompanies: targetCompanies,
	}
	
	sourceNames := s.scraper.SourceNames()

	// Initialize progress tracker
	s.scrapeProgMu.Lock()
	s.scrapeProg = scrapeProgress{
		Running:      true,
		StartTime:    time.Now(),
		SourcesTotal: len(sourceNames),
		SourcesDone:  0,
		JobsFound:    0,
		SourceStatus: make(map[string]string),
	}
	for _, n := range sourceNames {
		s.scrapeProg.SourceStatus[n] = "running"
	}
	s.scrapeProgMu.Unlock()

	w.WriteHeader(http.StatusAccepted)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "Background scraping initialized"})

	// Extract userID from request context before it goes out of scope safely
	userID := r.Context().Value("user_id").(string)

	// Background execution
	go func() {
		// Clean up the mutex lock and cancel func when the overall scrape finally stops
		defer func() {
			s.scrapeMutex.Lock()
			s.scrapeCancel = nil
			s.scrapeMutex.Unlock()
			cancel()
			// Mark progress as done
			s.scrapeProgMu.Lock()
			s.scrapeProg.Running = false
			s.scrapeProg.ElapsedSecs = int(time.Since(s.scrapeProg.StartTime).Seconds())
			s.scrapeProgMu.Unlock()
		}()

		// Run scrape with per-source progress callbacks
		jobs, err := s.scraper.ScrapeAllWithProgress(ctx, query, func(sourceName string, found int, isDone bool, isErr bool) {
			s.scrapeProgMu.Lock()
			defer s.scrapeProgMu.Unlock()
			s.scrapeProg.JobsFound += found
			
			if isDone {
				s.scrapeProg.SourcesDone++
				if isErr {
					s.scrapeProg.SourceStatus[sourceName] = "error"
				} else {
					s.scrapeProg.SourceStatus[sourceName] = "done"
				}
			}
			s.scrapeProg.ElapsedSecs = int(time.Since(s.scrapeProg.StartTime).Seconds())
		})
		if err != nil {
			if ctx.Err() == context.Canceled {
				log.Printf("Scraping was intentionally aborted by the user")
				return
			}
			log.Printf("Background scrape error: %v", err)
			return
		}

		added, err := s.db.SaveJobs(userID, jobs)
		if err != nil {
			log.Printf("Background db save error: %v", err)
			return
		}
		// Update final job count with the deduped result
		s.scrapeProgMu.Lock()
		s.scrapeProg.JobsFound = len(jobs)
		s.scrapeProgMu.Unlock()
		log.Printf("✅ Background scraping complete. Added %d new jobs.", added)
	}()
}

// GET /api/scrape/status — returns live telemetry for an in-progress scrape
func (s *server) handleScrapeStatus(w http.ResponseWriter, r *http.Request) {
	s.scrapeProgMu.RLock()
	prog := s.scrapeProg
	// Update elapsed live while still running
	if prog.Running && !prog.StartTime.IsZero() {
		prog.ElapsedSecs = int(time.Since(prog.StartTime).Seconds())
	}
	s.scrapeProgMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(prog)
}

// POST /api/vectorize
func (s *server) handleVectorizeJobs(w http.ResponseWriter, r *http.Request) {
	if s.ragPipeline == nil {
		http.Error(w, "RAG pipeline not initialized. Check Redis connection.", http.StatusInternalServerError)
		return
	}

	userID := r.Context().Value("user_id").(string)
	
	// Fetch all jobs for the tenant
	jobs, err := s.db.GetAllJobs(userID)
	if err != nil {
		http.Error(w, "Failed to read database state", http.StatusInternalServerError)
		return
	}
	if len(jobs) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "Skipped", "message": "No jobs found to vectorize."})
		return
	}

	w.WriteHeader(http.StatusAccepted)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "Background vectorization initialized"})

	// Run vector ingest in background
	go func() {
		fmt.Printf("📚 Backgrounding Redis Vector Ingestion for %d jobs...\n", len(jobs))
		_, err := s.ragPipeline.IngestJobs(context.Background(), "jobs", jobs)
		if err != nil {
			fmt.Printf("⚠️ Redis ingestion error: %v\n", err)
		} else {
			fmt.Printf("✅ Redis Vector Ingestion Complete!\n")
		}
	}()
}

// GET /api/resumes
func (s *server) handleListResumes(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	
	resumes, err := s.db.ListUserResumes(userID)
	if err != nil {
		http.Error(w, "Failed to load resumes", http.StatusInternalServerError)
		return
	}
	if resumes == nil {
		resumes = []store.UserResume{}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resumes)
}

// DELETE /api/resumes/{id}
func (s *server) handleDeleteResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := r.Context().Value("user_id").(string)

	if err := s.db.DeleteUserResume(userID, id); err != nil {
		http.Error(w, "Failed to delete resume", http.StatusInternalServerError)
		return
	}
	
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
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

	templateID := r.URL.Query().Get("template")
	var baseResumeRaw string
	
	if templateID != "" {
		baseResumeRaw, err = s.db.GetUserResumeContent(userID, templateID)
	}
	if err != nil || templateID == "" {
		// Fallback to project history if no DB resume provided
		b, err2 := os.ReadFile(projectHistoryPath)
		if err2 != nil {
			http.Error(w, "No resume found in DB and no fallback project_history.md exists. Please upload a base resume via User Profile & Setup.", http.StatusUnprocessableEntity)
			return
		}
		baseResumeRaw = string(b)
	}

	if s.ragPipeline == nil {
		http.Error(w, "RAG pipeline not initialized", http.StatusInternalServerError)
		return
	}

	// Update status to processing
	if err := s.db.UpdateTailoringStatus(userID, id, "processing"); err != nil {
		log.Printf("⚠️ Failed to update tailoring status: %v\n", err)
	}

	instructions := r.URL.Query().Get("instructions")

	fmt.Printf("📝 Background Tailoring initiated for %s @ %s [Tenant: %s]\n", job.Title, job.Company, userID)
	
	// Kick off background tailoring with a hard 15-minute timeout.
	// If the LLM hangs (Ollama stall, network drop, etc.) the context cancels
	// and the job is marked 'failed' rather than stuck in 'processing' forever.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		linkedInUrl, _ := s.db.GetSetting(userID, "linkedin_url")

		result, err := s.aligner.TailorResume(ctx, job, string(baseResumeRaw), linkedInUrl, instructions)
		if err != nil {
			log.Printf("❌ Background tailoring failed for %s: %v\n", id, err)
			s.db.UpdateTailoringStatus(userID, id, "failed")
			return
		}

		if err := s.db.SaveTailoredResult(userID, job.ID, result.TailoredResume, result.Report, result.FitBrief, result.MarketSalary, result.Score, result.SubScores["Technical"], result.SubScores["Domain"], result.SubScores["Seniority"], result.SubScores["Location"], result.SubScores["Lateral"], result.CoverLetter); err != nil {
			log.Printf("⚠️ Failed to persist tailoring data: %v\n", err)
			s.db.UpdateTailoringStatus(userID, id, "failed")
		}
		fmt.Printf(" ✅ Tailoring complete for %s\n", id)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "processing", "message": "Tailoring job queued locally", "id": id})
}

// POST /api/jobs/apply/{id}
func (s *server) handleSetApplied(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, "missing job id", http.StatusBadRequest)
		return
	}

	userID := r.Context().Value("user_id").(string)
	
	var req struct {
		Applied bool `json:"applied"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON payload", http.StatusBadRequest)
		return
	}

	if err := s.db.UpdateAppliedStatus(userID, id, req.Applied); err != nil {
		http.Error(w, "Failed to update applied status", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "success", "applied": req.Applied})
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

	// Enrich response with tailoring history
	history, _ := s.db.GetTailoringHistory(userID, id)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"job":     job,
		"history": history,
	})
}

// GET /api/jobs/history/{id} — returns tailoring score history for a specific job
func (s *server) handleTailoringHistory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userID := r.Context().Value("user_id").(string)

	history, err := s.db.GetTailoringHistory(userID, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if history == nil {
		history = []store.TailoringHistoryRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(history)
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

// GET /api/models — returns available Ollama models and current selection
func (s *server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models, err := llm.ListAvailableModels()
	if err != nil {
		http.Error(w, "Failed to list Ollama models: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	fast, deep := llm.ActiveModelNames()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"models":     models,
		"active_fast": fast,
		"active_deep": deep,
	})
}

// POST /api/models/configure — hot-swap model tiers and persist selection
func (s *server) handleConfigureModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Fast string `json:"fast"`
		Deep string `json:"deep"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}
	if req.Fast == "" || req.Deep == "" {
		http.Error(w, "Both 'fast' and 'deep' model names are required", http.StatusBadRequest)
		return
	}

	// Persist to SQLite so the selection survives server restarts
	userID := r.Context().Value("user_id").(string)
	_ = s.db.SaveSetting(userID, "llm_fast_model", req.Fast)
	_ = s.db.SaveSetting(userID, "llm_deep_model", req.Deep)
	// Also save under "system" scope so startup can read it without a user context
	_ = s.db.SaveSetting("system", "llm_fast_model", req.Fast)
	_ = s.db.SaveSetting("system", "llm_deep_model", req.Deep)

	// Hot-swap the live models
	if err := llm.ConfigureModels(req.Fast, req.Deep); err != nil {
		http.Error(w, "Model swap failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
		"fast":   req.Fast,
		"deep":   req.Deep,
	})
}

// GET /api/health — returns real-time status of all infrastructure components
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	type ComponentStatus struct {
		Status  string `json:"status"`  // "ok" | "degraded" | "error"
		Detail  string `json:"detail"`
		Latency string `json:"latency,omitempty"`
	}
	type HealthReport struct {
		Overall    string                     `json:"overall"`
		Components map[string]ComponentStatus `json:"components"`
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

		// Build installed model set
		installedSet := make(map[string]bool)
		var installedNames []string
		for _, m := range tags.Models {
			installedNames = append(installedNames, m.Name)
			installedSet[m.Name] = true
		}

		// Both tier models must be installed
		fastM, deepM := llm.ActiveModelNames()
		requiredModels := []string{fastM, deepM}
		var missing []string
		for _, req := range requiredModels {
			found := false
			for name := range installedSet {
				if strings.HasPrefix(name, req) {
					found = true
					break
				}
			}
			if !found {
				missing = append(missing, req)
			}
		}
		if len(missing) > 0 {
			allOk = false
			components["ollama"] = ComponentStatus{
				Status:  "degraded",
				Detail:  fmt.Sprintf("Missing models: %s. Installed: %s", strings.Join(missing, ", "), strings.Join(installedNames, ", ")),
				Latency: fmt.Sprintf("%dms", latency),
			}
			return
		}

		// Check which models are currently loaded in memory via /api/ps
		loadedModels := make(map[string]bool)
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
				loadedModels[m.Name] = true
			}
		}

		var loadedList, notLoadedList []string
		for _, req := range requiredModels {
			found := false
			for name := range loadedModels {
				if strings.HasPrefix(name, req) {
					found = true
					break
				}
			}
			if found {
				loadedList = append(loadedList, req)
			} else {
				notLoadedList = append(notLoadedList, req)
			}
		}

		status := "ok"
		var detail string
		if len(notLoadedList) > 0 {
			status = "degraded"
			detail = fmt.Sprintf("Installed ✓ | In memory: [%s] | Preloading: [%s]",
				strings.Join(loadedList, ", "), strings.Join(notLoadedList, ", "))
			allOk = false
		} else {
			detail = fmt.Sprintf("Both models in memory ✓ fast=%s deep=%s", requiredModels[0], requiredModels[1])
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

// GET /api/report/trends — returns time-series trend data for the trends dashboard
func (s *server) handleTrendReport(w http.ResponseWriter, r *http.Request) {
	userID := r.Context().Value("user_id").(string)
	report, err := s.db.GetTrendData(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(report)
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

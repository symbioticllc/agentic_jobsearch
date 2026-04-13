# Agentic Job Search

An AI-powered job scraping and resume alignment system. Scrapes jobs from multiple sources, stores them in a local SQLite database, and uses a cascading LLM + RAG pipeline to tailor your resume to each job description — all from a local web UI running at `http://localhost:8081`.

## What it does

1. **Scrapes** job listings from RemoteOK, Hacker News "Who is Hiring?", and direct ATS portals (Workday, Greenhouse, Lever, Ashby — with headless Chrome + LLM fallback for unknown parsing)
2. **Stores** all jobs in a local SQLite database with full-text search (FTS5)
3. **Indexes** job descriptions into Redis Stack for semantic vector search
4. **Ingests** context from base resumes, Brag Sheets, and LinkedIn URLs via the Profile Management UI — accepts `.pdf`, `.docx`, `.md`, and public **Google Docs URLs**
5. **Tailors** your resume for a specific job using RAG-augmented LLM generation — pulls the most relevant sections of your project history and targets the job's ATS keywords
6. **Scores** each job on a 0–100 fit scale across 5 dimensions (Technical, Domain, Seniority, Location, Lateral Pivot); auto-penalizes roles below target compensation
7. **Saves** a tailored resume, customized cover letter, and an alterations report for every application
8. **Exports** to Google Docs (`.docx`) via the Google Drive API

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    Web UI (port 8081)                │
│              HTML / CSS / Vanilla JS                 │
└─────────────────┬───────────────────────────────────┘
                  │ HTTP
┌─────────────────▼───────────────────────────────────┐
│              Go HTTP Server                          │
│  GET  /api/jobs              — list all jobs         │
│  POST /api/scrape            — run all scrapers      │
│  POST /api/scrape/stop       — cancel active scrape  │
│  POST /api/jobs/tailor/{id}  — tailor resume for job │
│  GET  /api/jobs/status/{id}  — poll tailoring state  │
│  GET  /api/jobs/search?q=    — semantic + FTS search │
│  GET  /api/resumes           — list uploaded resumes │
│  GET  /api/report/companies  — per-company analytics │
│  GET  /api/profile           — get profile settings  │
│  POST /api/profile           — save profile settings │
│  POST /api/profile/upload    — upload resume/bragsheet│
│  POST /api/export/gdocs/{id} — export to Google Docs │
└──────┬──────────┬────────────────┬───────────────────┘
       │          │                │
┌──────▼──┐  ┌───▼────┐  ┌────────▼────────┐
│ Scraper │  │ SQLite │  │   RAG Pipeline  │
│ Manager │  │  Store │  │ Redis Stack+LLM │
└──┬──┬───┘  └────────┘  └────────┬────────┘
   │  │  │                        │
   │  │  └─ Direct ATS            │
   │  │     (Greenhouse / Lever / │
   │  │      Ashby / DDG+Chrome)  │
   │  └──── HN Who's Hiring       │
   └─────── RemoteOK              │
                          ┌───────▼──────┐
                          │   Aligner    │
                          │ (LLM+Resume) │
                          └──────────────┘
```

## LLM Architecture

Single local model handles all generation tasks, with cloud providers as failover:

| Tier | Model | Purpose |
|------|-------|------|
| **Primary** | `qwen3:30b-a3b` (local Ollama) | All tasks: extraction, scoring, resume tailoring, cover letters |
| **Cloud failover 1** | Gemini 1.5 Flash (`GEMINI_API_KEY`) | Backup if local model fails |
| **Cloud failover 2** | Claude 3.5 Sonnet (`ANTHROPIC_API_KEY`) | Secondary backup |

Qwen3 30B-A3B is a MoE model with only ~3B active parameters per token, making it fast enough to handle both quick extraction tasks and deep structured reasoning without a separate fast-tier model.

The model is **preloaded and pinned in memory** (`keep_alive=-1`) on startup. This is particularly important if your model weights live on a NAS — the cold-load pull happens once at boot rather than stalling the first tailoring request.

## Prerequisites

- [Go 1.21+](https://go.dev/)
- [Ollama](https://ollama.com/) running locally on port `11434`
  ```bash
  ollama pull qwen3:30b-a3b      # Primary model — all generation tasks
  ollama pull nomic-embed-text   # Embeddings for RAG vector search
  ```
- [Docker](https://www.docker.com/) (for Redis Stack vector store)
- [Google Chrome](https://www.google.com/chrome/) installed (used by headless scraper fallback)

## Getting started

```bash
# 1. Clone and install dependencies
git clone <repo>
cd agentic-jobs
go mod download

# 2. Start everything
./start.sh
```

`start.sh` will automatically:
- Start the Redis Stack Docker container (creates it on first run)
- Verify Ollama is reachable on port `11434`
- Compile and run the Go backend

```bash
# 3. Open the UI
open http://localhost:8081

# 4. First-time setup (in the UI)
#    - Click "⚙️ User Profile & Setup"
#    - Save your LinkedIn Profile URL
#    - Upload a Base Resume (.md, .pdf, .docx, or Google Docs URL)
#    - Upload a Brag Sheet (ingested into RAG for tailoring context)

# 5. Check system health
#    - Click the ❤️ button in the top-right header
#    - Confirms Ollama (qwen3 loaded), Redis, and SQLite are all green

# 6. Optionally add target companies for Direct ATS scraping
#    Edit target_companies/companies.txt — one company name per line
```

## Shutting down

```bash
# Stop the Go server
Ctrl+C

# Stop Redis (optional — preserves vector index between sessions)
docker stop agentic-redis

# Stop Ollama / unload qwen3 from GPU memory (optional)
pkill ollama
```

### Hot reload (development)

```bash
go install github.com/air-verse/air@latest
air -c .air.toml
```

### Docker (experimental)

```bash
docker compose up
```

> Note: The Docker setup uses port 8080 and bundles Redis. Ollama still needs to run on the host.

## Configuration

| File | Purpose |
|------|---------|
| `experience/<userId>/base_resume.md` | Base resume the aligner rewrites — upload via Profile UI |
| `experience/<userId>/brag_sheet.md` | Detailed career context chunked into RAG |
| `project_history.md` | Fallback career history (ingested into RAG at startup) |
| `target_companies/*.txt` | One company name per line — used by the Direct ATS scraper |

Keywords used to filter scraped jobs have a broad default set in `main.go`.

You can **dynamically add keywords from the UI** by typing them (comma-separated) into the text box next to the Scrape button. They are instantly merged with the default base search.

Checking the **Scan Executive Roles** toggle in the UI adds 13 supplemental C-Suite and leadership titles (`CIO`, `CTO`, `SVP`, `Head Of`, etc.) to the search matrix.

## Scrapers

| Source | Method |
|--------|--------|
| **RemoteOK** | Public JSON API |
| **HN Who's Hiring** | HN Algolia API + Firebase HN API (monthly thread) |
| **Workday** | DuckDuckGo discovery → Hybrid Direct Native JSON API mapping (`wday/cxs/...`) |
| **Greenhouse** | `boards-api.greenhouse.io` — guesses company slug |
| **Lever** | `api.lever.co` — guesses company slug |
| **Ashby** | `api.ashbyhq.com/posting-api` — guesses company slug |
| **Agent Crawler** | DuckDuckGo Lite search → headless Chrome render → LLM extraction (fallback for unknown portals/Cloudflare blocks) |

The Direct scraper runs concurrently. It intelligently identifies Workday portals, searches for the correct subdomain using DDG, and hits undocumented native JSON APIs to bypass headless browser timeouts. It tries known ATS schemas in priority order before falling back to the LLM agent.

## Output

After tailoring a job, results are saved to SQLite and optionally exported:

```
queued_resume/
  085_Acme_Staff_Engineer.md          # Tailored resume in Markdown
  085_Acme_Staff_Engineer_REPORT.md   # Alteration report: what changed and why

potential-jobs/
  085_Acme_Staff_Engineer.md          # Job profile: score, fit brief, original JD
```

Files are prefixed with the fit score (0–100) for easy sorting. Use the **Export to Google Docs** button in the UI to push any tailored resume directly to your Google Drive as a `.docx`.

## Project structure

```
.
├── cmd/agentic-job-search/main.go   # Server entrypoint, HTTP routes
│   Routes:
│     GET  /api/jobs                 — list all jobs
│     POST /api/scrape               — run all scrapers
│     POST /api/scrape/stop          — cancel active scrape
│     POST /api/jobs/tailor/{id}     — tailor resume (background)
│     GET  /api/jobs/status/{id}     — poll tailoring status
│     GET  /api/jobs/search?q=       — semantic + FTS hybrid search
│     GET  /api/resumes              — list uploaded resume templates
│     GET  /api/report/companies     — per-company analytics
│     GET  /api/profile              — get profile settings
│     POST /api/profile              — save profile settings
│     POST /api/profile/upload       — upload resume or brag sheet
│     POST /api/export/gdocs/{id}    — export tailored resume to Google Docs
│     GET  /api/health               — system health check (no auth)
├── internal/
│   ├── aligner/    # Resume tailoring: RAG context + LLM prompt + output parsing
│   ├── drive/      # Google Drive / Docs export
│   ├── llm/        # Ollama (qwen3:30b-a3b) + Gemini + Claude client
│   ├── rag/        # Redis Vector client, embedder, markdown chunker
│   ├── scraper/    # RemoteOK, HN, Direct ATS (Greenhouse/Lever/Ashby/Agent) scrapers
│   └── store/      # SQLite persistence + FTS5 search
├── ui/             # Frontend: index.html, app.js, index.css
├── experience/     # Per-user resume and brag sheet storage
├── project_history.md              # Fallback career history for RAG
├── target_companies/               # Company lists for direct scraping
├── start.sh                        # Startup bootstrapper
├── potential-jobs/                 # Generated job profiles (gitignored)
├── queued_resume/                  # Generated tailored resumes (gitignored)
└── data/jobs.db                    # SQLite database (gitignored)
```

# Agentic Job Search

An AI-powered job scraping and resume alignment system. Scrapes jobs from multiple sources, stores them in a local database, and uses a RAG pipeline to tailor your resume to each job description — all from a local web UI.

## What it does

1. **Scrapes** job listings from RemoteOK, Hacker News "Who is Hiring?", and direct company ATS portals (Greenhouse, Lever, with a DuckDuckGo + LLM fallback for others)
2. **Stores** all jobs in a local SQLite database
3. **Indexes** job descriptions into Redis Stack for semantic search
4. **Tailors** your resume for a specific job using RAG-augmented LLM generation — pulls the most relevant sections of your project history and rewrites your resume to match the JD
5. **Scores** each job on a 0–100 fit scale, with compensation awareness (penalizes roles significantly below your target)
6. **Saves** a tailored resume and alteration report to `queued_resume/` and a job profile to `potential-jobs/`

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                    Web UI (port 8081)                │
│              HTML / CSS / Vanilla JS                 │
└─────────────────┬───────────────────────────────────┘
                  │ HTTP
┌─────────────────▼───────────────────────────────────┐
│              Go HTTP Server                          │
│  GET  /api/jobs          — list all jobs             │
│  POST /api/scrape        — run all scrapers          │
│  POST /api/jobs/tailor/{id} — tailor resume for job  │
│  GET  /api/jobs/search?q — semantic job search       │
└──────┬──────────┬────────────────┬───────────────────┘
       │          │                │
┌──────▼──┐  ┌───▼────┐  ┌────────▼────────┐
│ Scraper │  │ SQLite │  │   RAG Pipeline  │
│ Manager │  │  Store │  │ Redis Stack+LLM │
└──┬──┬───┘  └────────┘  └────────┬────────┘
   │  │  │                        │
   │  │  └─ Direct ATS            │
   │  │     (Greenhouse/Lever/DDG)│
   │  └──── HN Who's Hiring       │
   └─────── RemoteOK              │
                          ┌───────▼──────┐
                          │   Aligner    │
                          │ (LLM+Resume) │
                          └──────────────┘
```

## Prerequisites

- [Go 1.25+](https://go.dev/)
- [Ollama](https://ollama.com/) running locally on port `11434`
  - `ollama pull gemma2:9b` — resume tailoring LLM
  - `ollama pull nomic-embed-text` — embeddings model
- [Redis Stack](https://redis.io/docs/about/about-stack/) running locally on port `6379`
  - `docker run -d --name redis-stack -p 6379:6379 -p 8001:8001 redis/redis-stack:latest`

## Getting started

```bash
# 1. Clone and install dependencies
git clone <repo>
cd agentic-jobs
go mod download

# 2. Start Redis Stack and Ollama (see prerequisites)

# 3. Add your resume content
#    - Edit experience/base_resume.md  (used as the base for tailoring)
#    - Edit project_history.md         (detailed history the RAG pipeline draws from)

# 4. Optionally add target companies
#    - Edit target_companies/companies.txt (one company name per line)

# 5. Run the server
go run ./cmd/agentic-job-search

# Open http://localhost:8081
```

### Hot reload (development)

```bash
# Install air
go install github.com/air-verse/air@latest

# Run with hot reload
air -c .air.toml
```

### Docker (experimental)

```bash
docker compose up
```

> Note: The Docker setup currently uses port 8080 and includes Redis. Ollama still needs to run on the host.

## Configuration

| File | Purpose |
|------|---------|
| `experience/base_resume.md` | Your resume in Markdown — the base template the aligner rewrites |
| `project_history.md` | Detailed career history chunked into RAG. More detail = better tailoring |
| `target_companies/` | `.txt` files with one company name per line — used by the Direct ATS scraper |

### Search keywords

Keywords used to filter jobs are hardcoded in `main.go`:

```go
Keywords: []string{"golang", "go", "backend", "architect", "staff", "principal"},
```

Edit these to match your target roles.

## Scrapers

| Source | Method |
|--------|--------|
| **RemoteOK** | Public JSON API |
| **HN Who's Hiring** | HN Algolia API + Firebase HN API (monthly thread) |
| **Direct ATS** | Greenhouse API → Lever API → DuckDuckGo search + LLM HTML parsing (fallback) |

The Direct scraper iterates over every company in `target_companies/`, tries Greenhouse and Lever APIs by guessing the company slug, and falls back to a DuckDuckGo search + LLM extraction if both fail.

## Output files

After tailoring a job:

```
potential-jobs/
  085_Acme_Staff_Engineer.md          # Job profile: score, fit brief, original JD

queued_resume/
  085_Acme_Staff_Engineer.md          # Tailored resume in Markdown
  085_Acme_Staff_Engineer_REPORT.md   # Alteration report: what changed and why
```

Files are prefixed with the fit score (0–100) for easy sorting.

## Project structure

```
.
├── cmd/agentic-job-search/main.go   # Server entrypoint, HTTP routes
├── internal/
│   ├── aligner/    # Resume tailoring: RAG context + LLM prompt + output parsing
│   ├── llm/        # Ollama wrapper (LangchainGo)
│   ├── rag/        # Redis Vector client, embedder, markdown chunker
│   ├── scraper/    # RemoteOK, HN, Direct ATS scrapers
│   └── store/      # SQLite persistence
├── ui/             # Frontend: index.html, app.js, index.css
├── experience/     # base_resume.md + resume docx
├── project_history.md              # Detailed career history for RAG
├── target_companies/               # Company lists for direct scraping
├── potential-jobs/                 # Generated job profiles (gitignored)
├── queued_resume/                  # Generated tailored resumes (gitignored)
└── data/jobs.db                    # SQLite database (gitignored)
```

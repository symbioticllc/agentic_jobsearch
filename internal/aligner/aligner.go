package aligner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/leee/agentic-jobs/internal/llm"
	"github.com/leee/agentic-jobs/internal/rag"
	"github.com/leee/agentic-jobs/internal/scraper"
)

// TailoredResult contains all the outputs of the tailoring process
type TailoredResult struct {
	Score          int
	SubScores      map[string]int
	MarketSalary   string
	FitBrief       string
	TailoredResume string
	Report         string
}

// Aligner handles the logic of tailoring a resume for a specific job
type Aligner struct {
	rag *rag.RAG
}

// NewAligner creates a new aligner instance
func NewAligner(r *rag.RAG) *Aligner {
	return &Aligner{
		rag: r,
	}
}

// TailorResume generates a tailored resume for a given job and returns a structured result
func (a *Aligner) TailorResume(ctx context.Context, job scraper.Job, baseResume string) (TailoredResult, error) {
	var result TailoredResult

	// 1. Get relevant context from RAG
	jdSnippet := job.Title + "\n" + job.Description
	if len(jdSnippet) > 2000 {
		jdSnippet = jdSnippet[:2000]
	}

	ragResults, err := a.rag.QueryRelevant(ctx, "project_history", jdSnippet, 5)
	if err != nil {
		return result, fmt.Errorf("rag query failed: %w", err)
	}

	var contextBuf strings.Builder
	for _, res := range ragResults {
		contextBuf.WriteString(fmt.Sprintf("\n### %s\n%s\n", res.Chunk.Header, res.Chunk.Content))
	}

	compStr := job.Compensation
	if compStr == "" {
		compStr = "Not provided"
	}

	// 2. Formulate the prompt
	prompt := fmt.Sprintf(`
You are an expert career coach and technical recruiter. Your task is to analyze a job description and tailor a professional resume.

### Output Requirements
1. **SCORE**: Provide a holistic job fit score from 0-100 based on the candidate's overall experience matching the JD. CRITICAL: The candidate requires ~$400k total compensation. If the job explicitly posts a compensation significantly lower (e.g. < $300k), penalize the score heavily. If compensation is "Not provided", do NOT factor compensation into your score calculation at all.
2. **SUB-SCORES**: Break down the fit into 3 sub-categories, providing a 0-100 score for exactly these three dimensions: "Technical", "Domain", and "Seniority".
3. **MARKET_SALARY**: Based strictly on the job title, company, and typical tech industry market rates for this seniority, output a realistic estimated compensation range (e.g. "$250k - $320k").
4. **BRIEF**: Provide a 1-2 sentence explanation of WHY the candidate is a good fit, or explicitly mention if the compensation might be a mismatch.
5. **RESUME**: Provide the full tailored resume in Markdown. Bolt on specific projects from the Context that match JD requirements.
6. **REPORT**: A list of specific alterations made.

--- 
### Job Description
Title: %s
Company: %s
Compensation: %s
Description: %s

### Base Resume
%s

### Experience Context (Use these specific projects/details!)
%s

---
Return your response in this EXACT format:
SCORE: [holistic score]
SUB_SCORES:
Technical: [score]
Domain: [score]
Seniority: [score]
MARKET_SALARY: [est salary range]
BRIEF: [brief]
RESUME: 
[full resume]
---REPORT---
[alterations list]
`, job.Title, job.Company, compStr, job.Description, baseResume, contextBuf.String())

	// 3. Call LLM
	response, err := llm.Generate(ctx, prompt)
	if err != nil {
		return result, fmt.Errorf("llm generate failed: %w", err)
	}

	// 4. Parse response
	result.Score = parseScore(response)
	result.SubScores = parseSubScores(response)
	result.MarketSalary = parseMarketSalary(response)
	result.FitBrief = parseBrief(response)

	parts := strings.Split(response, "---REPORT---")
	if len(parts) >= 2 {
		result.Report = strings.TrimSpace(parts[1])
		resumePart := strings.SplitN(parts[0], "RESUME:", 2)
		if len(resumePart) >= 2 {
			result.TailoredResume = cleanMarkdownTraces(strings.TrimSpace(resumePart[1]))
		}
	} else {
		result.TailoredResume = cleanMarkdownTraces(response)
		result.Report = "No separate report found."
	}

	return result, nil
}

// cleanMarkdownTraces removes structural LLM artifacts like ```markdown
func cleanMarkdownTraces(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```markdown") {
		s = strings.TrimPrefix(s, "```markdown")
	} else if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSuffix(s, "```")
	}
	return strings.TrimSpace(s)
}

func parseScore(s string) int {
	re := regexp.MustCompile(`(?i)SCORE:\s*(\d+)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		val, _ := strconv.Atoi(match[1])
		return val
	}
	return 0
}

func parseSubScores(s string) map[string]int {
	scores := make(map[string]int)
	lines := strings.Split(s, "\n")
	
	inSubScores := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(line), "SUB_SCORES:") {
			inSubScores = true
			continue
		}
		if inSubScores && strings.HasPrefix(strings.ToUpper(line), "BRIEF:") {
			break
		}
		if inSubScores && strings.Contains(line, ":") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				vStr := strings.TrimSpace(parts[1])
				// Clean non-numeric characters from vStr (like if they output " [85]" or "85/100")
				re := regexp.MustCompile(`\d+`)
				numStr := re.FindString(vStr)
				if numStr != "" {
					val, _ := strconv.Atoi(numStr)
					scores[k] = val
				}
			}
		}
	}
	
	return scores
}

func parseBrief(s string) string {
	re := regexp.MustCompile(`(?i)BRIEF:\s*([^\n]+)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return "No brief provided."
}

func parseMarketSalary(s string) string {
	re := regexp.MustCompile(`(?i)MARKET_SALARY:\s*([^\n]+)`)
	match := re.FindStringSubmatch(s)
	if len(match) > 1 {
		return strings.TrimSpace(match[1])
	}
	return "Unknown"
}

// SaveJobProfile saves the JD, fit brief, and score to the potential-jobs folder using a SHA256 identity
func SaveJobProfile(job scraper.Job, result TailoredResult) error {
	dir := "./potential-jobs"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(job.ID))
	signature := fmt.Sprintf("%x", hash)
	filename := fmt.Sprintf("%s.md", signature)
	path := filepath.Join(dir, filename)

	content := fmt.Sprintf(`# Job Profile: %s @ %s

## Fit Score (Holistic): %d%%
_Technical: %d%% | Domain: %d%% | Seniority: %d%%_

## Market Estimated Salary
**%s**

## Why you're a match: 
%s

## Original Job Description
Source: %s
URL: %s

%s
`, job.Title, job.Company, result.Score, result.SubScores["Technical"], result.SubScores["Domain"], result.SubScores["Seniority"], result.MarketSalary, result.FitBrief, job.Source, job.URL, job.Description)

	return os.WriteFile(path, []byte(content), 0644)
}

// SaveTailoredResume saves the content and report to the queued_resume folder using a SHA256 identity
func SaveTailoredResume(job scraper.Job, result TailoredResult) error {
	dir := "./queued_resume"
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	hash := sha256.Sum256([]byte(job.ID))
	signature := fmt.Sprintf("%x", hash)
	
	resumeFile := filepath.Join(dir, signature+".md")
	reportFile := filepath.Join(dir, signature+"_REPORT.md")

	if err := os.WriteFile(resumeFile, []byte(result.TailoredResume), 0644); err != nil {
		return err
	}
	return os.WriteFile(reportFile, []byte(result.Report), 0644)
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, " ", "_")
	s = strings.ReplaceAll(s, "/", "-")
	reg, _ := regexp.Compile("[^a-zA-Z0-9_-]+")
	return reg.ReplaceAllString(s, "")
}

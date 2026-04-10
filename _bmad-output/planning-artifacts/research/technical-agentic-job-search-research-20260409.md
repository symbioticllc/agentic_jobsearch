---
stepsCompleted: [1]
inputDocuments: []
workflowType: 'research'
lastStep: 1
research_type: 'Technical Research'
research_topic: 'Agentic Job Search Architecture'
research_goals: 'Determine language choice (Go preference), RAG necessity, and infrastructure vs laptop requirements.'
user_name: 'Lee'
date: '2026-04-09'
web_research_enabled: true
source_verification: true
---

# Research Report: Technical Research

**Date:** 2026-04-09
**Author:** Lee
**Research Type:** Technical Research

---

## Research Overview

## Technical Research Scope Confirmation

**Research Topic:** Agentic Job Search Architecture
**Research Goals:** Determine language choice (Go preference), RAG necessity, and infrastructure vs laptop requirements. Maximize comprehensiveness while minimizing cost (solo developer/job seeker).

**Technical Research Scope:**

- Architecture Analysis - design patterns, frameworks, system architecture
- Implementation Approaches - development methodologies, coding patterns
- Technology Stack - languages, frameworks, tools, platforms
- Integration Patterns - APIs, protocols, interoperability
- Performance Considerations - scalability, optimization, patterns
- Cost Analysis - Open source vs paid, local vs cloud, solo developer constraints

**Research Methodology:**

- Current web data with rigorous source verification
- Multi-source validation for critical technical claims
- Confidence level framework for uncertain information
- Comprehensive technical coverage with architecture-specific insights

**Scope Confirmed:** 2026-04-09

---

## Technology Stack Analysis

### Programming Languages

Python is the undisputed industry standard for AI, NLP, and agentic workflows, featuring libraries like Scrapy, BeautifulSoup, and full-featured LangChain. However, **Go (Golang)** is a highly viable and high-performing alternative. 
_Popular Languages: Go, Python_
_Language Evolution: Go is rising for AI agents needing high concurrency, with frameworks like LangChainGo bridging the gap._
_Solo Dev Context: While Python is faster for prototyping due to its ecosystem, Go is unmatched for high-throughput scaling natively. If you prefer Go, `LangChainGo` allows you to orchestrate LLM agents securely._
_Source: https://github.com/tmc/langchaingo_

### Development Frameworks and Libraries

To build the Agentic workflow, you need an orchestration layer and a web scraping layer.
_Major Frameworks: LangChainGo (Orchestration), Colly & chromedp (Go Scraping), Playwright (Python/Go bindings)_
_Micro-frameworks: MCP (Model Context Protocol) servers to standardize how the LLM interacts with tools._
_Ecosystem Maturity: Python has a more mature ecosystem, but Go's concurrency model makes scraper tools remarkably fast._
_Source: https://github.com/gocolly/colly_

### Database and Storage Technologies (Including RAG)

You asked: "Do I need a RAG?" Yes—RAG (Retrieval-Augmented Generation) is foundational here. To dynamically build a resume, you need a Vector Database to store chunks of your `project_history.md` so the LLM can query the *most semantically relevant* skills for a specific Job Description. 
_Relational Databases: SQLite (Local, $0 cost)_
_NoSQL Databases: ChromaDB or Milvus (Vector Databases for RAG)_
_In-Memory Databases: Redis (Optional for caching scrape results)_
_Solo Dev Context: ChromaDB is free, open-source, and runs locally. It acts as the RAG backend without any cloud DB fees._
_Source: https://www.trychroma.com/_

### Development Tools and Platforms

_IDE and Editors: Any IDE (like Cursor/Claude Code) alongside the BMad framework._
_Local LLM Execution: Ollama_
_Solo Dev Context: You can use Ollama to run models like Llama-3 or Mistral locally for zero cost. If local hardware struggles with complex context windows, using an API like Gemini Flash or Anthropic Haiku costs fractions of a cent per request._
_Source: https://ollama.com/_

### Cloud Infrastructure vs Local Hardware

You asked: "Can I run this from my laptop or do I need infrastructure?" You can run this **100% locally from your laptop**.
_Infrastructure: Local Laptop (Mac with 16GB+ RAM is ideal)_
_Cost Analysis: $0 for software (Ollama + ChromaDB + Go). Local hardware handles embedding generation (Chroma) and inference (Ollama)._
_Migration Patterns: By building the system in Go on your local machine, the memory overhead stays negligible compared to Python or browser environments._

<!-- Content will be appended sequentially through research workflow steps -->

#!/bin/bash

# start.sh - Bootstrapper for Agentic Job Search Architecture

echo "==============================================="
echo "   Agentic Job Search - Startup Bootstrapper   "
echo "==============================================="
echo ""

# 1. Dependency Checks & Setup

# Source user's shell profile to pick up API keys (ANTHROPIC_API_KEY, GEMINI_API_KEY, etc.)
[ -f "$HOME/.zshrc" ] && source "$HOME/.zshrc" 2>/dev/null

# Ensure all tools are findable regardless of calling shell / IDE environment
export PATH="/usr/local/bin:/opt/homebrew/bin:/Applications/Docker.app/Contents/Resources/bin:/Applications/Ollama.app/Contents/MacOS:$PATH"
export GOPATH="$HOME/go"
export PATH="$GOPATH/bin:/usr/local/go/bin:$PATH"

if ! command -v docker >/dev/null 2>&1; then
    echo "⚠️  WARNING: Docker is not installed or not in PATH."
    echo "This application uses Redis Stack via Docker for local vector RAG."
    echo "Please ensure you have Redis-Stack running on port 6379, or install Docker."
    echo ""
else
    # Check if agentic-redis container is running
    if ! docker ps | grep -q "agentic-redis"; then
        echo " -> Checking Redis Stack Container..."
        # If it doesn't exist, create and run it
        if ! docker ps -a | grep -q "agentic-redis"; then
            echo " -> Starting Docker Redis Stack Vector Engine for the first time..."
            docker run -d --name agentic-redis -p 6379:6379 -p 8001:8001 redis/redis-stack-server:latest
        else
            echo " -> Starting existing Docker Redis Stack Container..."
            docker start agentic-redis
        fi
    else
        echo " ✅ Redis Stack is already running in Docker."
    fi
fi

# 2. Check Ollama natively
if curl -s -f http://localhost:11434/api/tags > /dev/null; then
    echo " ✅ Ollama LLM provider detected natively on port 11434."
else
    echo "⚠️  WARNING: Ollama is not actively running on localhost:11434!"
    echo "Make sure Ollama is open natively on your host machine to compile Resumes."
    echo "If Ollama crashes during execution, the server will fallback to Anthropic (requires API key)."
    echo ""
fi

# 3. Compile and Run the Go Backend server
echo ""
echo "🚀 Booting up the Agentic Server natively..."

# Clear any stale process holding port 8081 from a previous session
STALE_PID=$(lsof -ti :8081 2>/dev/null)
if [ -n "$STALE_PID" ]; then
    echo " -> Clearing stale process on :8081 (PID $STALE_PID)..."
    kill -9 $STALE_PID 2>/dev/null
    sleep 0.5
fi

go run ./cmd/agentic-job-search/main.go

#!/bin/bash

# start.sh - Bootstrapper for Agentic Job Search Architecture

echo "==============================================="
echo "   Agentic Job Search - Startup Bootstrapper   "
echo "==============================================="
echo ""

# Ensure all tools are findable regardless of calling shell / IDE environment
export PATH="/usr/local/bin:/opt/homebrew/bin:/Applications/Docker.app/Contents/Resources/bin:/Applications/Ollama.app/Contents/MacOS:$PATH"
export GOPATH="$HOME/go"
export PATH="$GOPATH/bin:/usr/local/go/bin:$PATH"

# 1. Environment & API Key Management
if [ -f "$HOME/.zshrc" ]; then
    source "$HOME/.zshrc" 2>/dev/null
fi

if [ ! -f "./.env" ]; then
    echo "⚙️  Initializing environment file..."
    echo -n "Enter your ANTHROPIC_API_KEY (or press Enter to skip): "
    read -r ANT_KEY
    echo -n "Enter your GEMINI_API_KEY (or press Enter to skip): "
    read -r GEM_KEY
    echo "ANTHROPIC_API_KEY=\"$ANT_KEY\"" > ./.env
    echo "GEMINI_API_KEY=\"$GEM_KEY\"" >> ./.env
    echo " ✅ Generated ./.env"
    echo ""
fi

# Apply local .env overrides safely
set -a
source ./.env 2>/dev/null
set +a

# 2. Redis Vector Engine (Docker)
if ! command -v docker >/dev/null 2>&1; then
    echo "⚠️  WARNING: Docker is not installed or not in PATH."
    echo "Please ensure you have Redis-Stack running natively on port 6379, or install Docker."
    echo ""
else
    if ! docker ps | grep -q "agentic-redis"; then
        echo " -> Checking Redis Stack Container..."
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

# 3. Native Ollama Engine & Model Check
MODEL_NAME="gemma4:e4b"
if curl -s -f http://localhost:11434/api/tags > /dev/null; then
    echo " ✅ Ollama already running on port 11434."
else
    echo " ⚡ Ollama not detected — starting ollama serve in background..."
    ollama serve > /tmp/ollama.log 2>&1 &
    OLLAMA_PID=$!
    echo " -> Waiting for Ollama to become ready (PID $OLLAMA_PID)..."
    WAIT=0
    until curl -s -f http://localhost:11434/api/tags > /dev/null 2>&1; do
        if [ $WAIT -ge 30 ]; then
            echo " ⚠️  Ollama did not respond within 30s — continuing anyway (check /tmp/ollama.log)"
            break
        fi
        sleep 1
        WAIT=$((WAIT + 1))
    done
    if curl -s -f http://localhost:11434/api/tags > /dev/null 2>&1; then
        echo " ✅ Ollama is ready."
    fi
fi

echo " -> Verifying core model ($MODEL_NAME) is actively downloaded..."
if ! ollama list | grep -q "$MODEL_NAME"; then
    echo " ⚠️  Core model ($MODEL_NAME) is missing locally."
    echo " ⏳ Downloading model now (this may take several minutes)..."
    ollama pull "$MODEL_NAME"
    echo " ✅ Model downloaded successfully!"
else
    echo " ✅ Core model ($MODEL_NAME) confirmed locally available."
fi

# 4. Check for .gitignore security block
if ! grep -q "\.env" ./.gitignore 2>/dev/null; then
    echo ".env" >> ./.gitignore
    echo "🔒 Protected .env via .gitignore"
fi

# 5. Compile and Run the Go Backend server
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

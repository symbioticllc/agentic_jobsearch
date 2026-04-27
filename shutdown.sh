#!/bin/bash
echo "🛑 Stopping Agentic Jobs server gracefully..."
# Send SIGTERM to the running agentic-job-search process
pkill -SIGTERM -f "agentic-job-search"
if [ $? -eq 0 ]; then
    echo "✅ Shutdown signal sent."
else
    echo "⚠️  No running agentic-job-search process found."
fi

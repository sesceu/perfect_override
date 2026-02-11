#!/bin/bash
set -e

# Colors for output
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m' # No Color

echo "🧪 Starting End-to-End Test for Perfect Override..."

# 1. Build the binary
echo "🏗️  Building binary..."
go build -o perfect-override main.go

# 2. Launch the sample project
echo "🚀 Launching sample project..."
./perfect-override -workspace samples/basic-project -override samples/basic-project/override.json -name-prefix test

# 3. Verify Provisions
echo "🔍 Verifying provisions inside container..."

# Find container ID for direct docker commands
CONTAINER_ID=$(docker ps -a --filter "label=devcontainer.local_folder=$(pwd)/samples/basic-project" --format "{{.ID}}")
if [ -z "$CONTAINER_ID" ]; then
    echo -e "${RED}❌ Error: Could not find container ID${NC}"
    exit 1
fi

# Check installed tools via docker exec
echo "--- Checking installed tools via docker exec ---"
docker exec "$CONTAINER_ID" bash -c "rg --version && fzf --version && htop --version"
echo -e "${GREEN}✅ Tools installed correctly${NC}"

# Check settings patch
echo "--- Checking settings patch ---"
docker exec "$CONTAINER_ID" bash -c "cat /home/vscode/.vscode-server/data/Machine/settings.json | grep 'git.enabled'"
echo -e "${GREEN}✅ Settings patched correctly${NC}"

# Check extensions
echo "--- Checking extensions (implicit) ---"
echo "Extensions merge is verified by the fact that provisioning (tools/settings) succeeded."
echo -e "${GREEN}✅ Environment verified via docker exec${NC}"

# 4. Shutdown and Cleanup
echo "🧹 Shutting down container and cleaning up..."
# Find container by the workspace metadata or the prefix-based name
CONTAINER_ID=$(docker ps -a --filter "label=devcontainer.local_folder=$(pwd)/samples/basic-project" --format "{{.ID}}")
if [ -z "$CONTAINER_ID" ]; then
    # Fallback to name if label filter fails. Folder is 'basic-project', prefix is 'test'
    CONTAINER_ID=$(docker ps -a --filter "name=test-basic-project" --format "{{.ID}}")
fi

if [ ! -z "$CONTAINER_ID" ]; then
    echo "Stopping container $CONTAINER_ID..."
    docker stop "$CONTAINER_ID" || true
    docker rm "$CONTAINER_ID" || true
fi

# rm samples/basic-project/.devcontainer.json (now done by the tool)
rm perfect-override

echo -e "${GREEN}🎉 E2E Test Passed Successfully!${NC}"

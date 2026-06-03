#!/usr/bin/env bash
set -euo pipefail

# Install Docker if not present
if ! command -v docker >/dev/null 2>&1; then
    echo "Installing Docker..."
    curl -fsSL https://get.docker.com | sh
    systemctl enable --now docker
    echo "Docker installed successfully"
else
    echo "Docker is already installed"
fi

# Install jq for health check output
if ! command -v jq >/dev/null 2>&1; then
    echo "Installing jq..."
    apt-get update -qq
    apt-get install -y jq >/dev/null
    echo "jq installed successfully"
else
    echo "jq is already installed"
fi

# Verify installations
echo "Docker version: $(docker --version)"
echo "Docker Compose version: $(docker compose version)"
echo "jq version: $(jq --version)"

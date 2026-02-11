FROM mcr.microsoft.com/devcontainers/base:ubuntu

# Install corporate required tools
RUN apt-get update && apt-get install -y \
    curl \
    git \
    && rm -rf /var/lib/apt/lists/*

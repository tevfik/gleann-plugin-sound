#!/bin/bash
# One-liner installer for gleann-plugin-sound (Linux/macOS)
set -e

# --- Colors for output ---
GREEN='\033[0;32m'
CYAN='\033[0;36m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

echo -e "${CYAN}=== Installing gleann-plugin-sound ===${NC}"

# Detect OS and Architecture
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
    x86_64) ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *)
        echo -e "${RED}Error: Unsupported architecture: $ARCH${NC}"
        exit 1
        ;;
esac

case "$OS" in
    linux|darwin) ;;
    *)
        echo -e "${RED}Error: Unsupported OS: $OS${NC}"
        exit 1
        ;;
esac

# Create install directory
INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"

# Resolve latest release version from GitHub API
REPO="tevfik/gleann-plugin-sound"
echo -e "${YELLOW}Fetching latest release info...${NC}"
LATEST_TAG=$(curl -s "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST_TAG" ]; then
    echo -e "${RED}Error: Could not retrieve latest release tag.${NC}"
    exit 1
fi

echo -e "${GREEN}Found release: $LATEST_TAG${NC}"

# Download URL
TARBALL="gleann-plugin-sound_${LATEST_TAG#v}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$LATEST_TAG/$TARBALL"

echo -e "${YELLOW}Downloading binary from: $URL${NC}"
TMP_DIR=$(mktemp -d)
curl -L "$URL" -o "$TMP_DIR/$TARBALL"

# Extract and install
echo -e "${YELLOW}Extracting to $INSTALL_DIR...${NC}"
tar -xzf "$TMP_DIR/$TARBALL" -C "$TMP_DIR"
mv "$TMP_DIR/gleann-plugin-sound" "$INSTALL_DIR/"
chmod +x "$INSTALL_DIR/gleann-plugin-sound"

# Clean up
rm -rf "$TMP_DIR"

echo -e "${GREEN}✓ gleann-plugin-sound installed successfully to $INSTALL_DIR/gleann-plugin-sound${NC}"
echo -e "${YELLOW}Starting configuration wizard...${NC}"
"$INSTALL_DIR/gleann-plugin-sound" tui

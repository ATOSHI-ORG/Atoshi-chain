#!/bin/bash

# ============================================================================
# Atoshi Chain - Genesis Initialization Script
# ============================================================================
# This script initializes the genesis file with 70 billion ATOS pre-mined
# for mainnet deployment.
#
# Usage:
#   ./scripts/init_genesis.sh [OPTIONS]
#
# Options:
#   --no-build    Skip the build step
#   --testnet     Use testnet chain ID (atoshi_88288-1)
#   --devnet      Use devnet chain ID (atoshi_88388-1)
#   -h, --help    Show help message
# ============================================================================

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default Configuration (Mainnet)
CHAIN_ID="${CHAIN_ID:-atoshi_88188-1}"
MONIKER="${MONIKER:-atoshi-node}"
KEYRING_BACKEND="${KEYRING_BACKEND:-test}"
HOME_DIR="${HOME_DIR:-$HOME/.atoshid}"

# Token configuration
# Total Supply: 10,000,000,000,000 ATOS (10 trillion)
# Pre-mine: 70,000,000,000 ATOS (70 billion)
# Base denom: aatos (1 ATOS = 10^18 aatos)
PREMINED_AMOUNT="70000000000000000000000000000aatos"  # 70 billion ATOS in aatos
BASE_DENOM="aatos"

# Build flag
DO_BUILD=true

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --no-build)
            DO_BUILD=false
            shift
            ;;
        --testnet)
            CHAIN_ID="atoshi_88288-1"
            HOME_DIR="$HOME/.atoshid-testnet"
            shift
            ;;
        --devnet)
            CHAIN_ID="atoshi_88388-1"
            HOME_DIR="$HOME/.atoshid-dev"
            shift
            ;;
        -h|--help)
            echo "Atoshi Chain Genesis Initialization Script"
            echo ""
            echo "Usage: ./scripts/init_genesis.sh [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --no-build    Skip the build step"
            echo "  --testnet     Use testnet chain ID (atoshi_88288-1)"
            echo "  --devnet      Use devnet chain ID (atoshi_88388-1)"
            echo "  -h, --help    Show this help message"
            echo ""
            echo "Environment Variables:"
            echo "  CHAIN_ID          Override chain ID"
            echo "  MONIKER           Node moniker (default: atoshi-node)"
            echo "  KEYRING_BACKEND   Keyring backend (default: test)"
            echo "  HOME_DIR          Data directory (default: ~/.atoshid)"
            echo ""
            exit 0
            ;;
        *)
            echo -e "${RED}Unknown option: $1${NC}"
            exit 1
            ;;
    esac
done

# Print banner
echo -e "${BLUE}"
echo "╔═══════════════════════════════════════════════════════════════════╗"
echo "║                                                                   ║"
echo "║     █████╗ ████████╗ ██████╗ ███████╗██╗  ██╗██╗                  ║"
echo "║    ██╔══██╗╚══██╔══╝██╔═══██╗██╔════╝██║  ██║██║                  ║"
echo "║    ███████║   ██║   ██║   ██║███████╗███████║██║                  ║"
echo "║    ██╔══██║   ██║   ██║   ██║╚════██║██╔══██║██║                  ║"
echo "║    ██║  ██║   ██║   ╚██████╔╝███████║██║  ██║██║                  ║"
echo "║    ╚═╝  ╚═╝   ╚═╝    ╚═════╝ ╚══════╝╚═╝  ╚═╝╚═╝                  ║"
echo "║                                                                   ║"
echo "║           Genesis Initialization Script                           ║"
echo "║                                                                   ║"
echo "╚═══════════════════════════════════════════════════════════════════╝"
echo -e "${NC}"

echo -e "${GREEN}=== Atoshi Chain Genesis Initialization ===${NC}"
echo ""
echo -e "${BLUE}Configuration:${NC}"
echo "  Chain ID:        $CHAIN_ID"
echo "  Moniker:         $MONIKER"
echo "  Home Directory:  $HOME_DIR"
echo "  Keyring Backend: $KEYRING_BACKEND"
echo "  Pre-mined:       70,000,000,000 ATOS (70 Billion)"
echo ""

# Validate dependencies
command -v jq >/dev/null 2>&1 || {
    echo -e "${RED}Error: jq not installed.${NC}"
    echo "  macOS: brew install jq"
    echo "  Ubuntu: sudo apt-get install jq"
    exit 1
}

# Build and install if requested
if [[ $DO_BUILD == true ]]; then
    echo -e "${BLUE}Step 1: Building and installing atoshid...${NC}"
    
    # Check if we're in the project root
    if [[ ! -f "Makefile" ]]; then
        # Try to find the project root
        SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
        PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
        cd "$PROJECT_ROOT"
    fi
    
    if [[ -f "Makefile" ]]; then
        make install
        echo -e "${GREEN}Build complete!${NC}"
    else
        echo -e "${RED}Error: Makefile not found. Please run from project root.${NC}"
        exit 1
    fi
else
    echo -e "${YELLOW}Skipping build step (--no-build flag)${NC}"
fi

# Verify atoshid is available
command -v atoshid >/dev/null 2>&1 || {
    echo -e "${RED}Error: atoshid not found in PATH.${NC}"
    echo "Please run 'make install' first or ensure GOPATH/bin is in your PATH."
    exit 1
}

echo ""
echo -e "${BLUE}Step 2: Removing existing data...${NC}"
rm -rf "$HOME_DIR"
echo -e "${GREEN}Done!${NC}"

echo ""
echo -e "${BLUE}Step 3: Initializing chain...${NC}"
atoshid init "$MONIKER" --chain-id "$CHAIN_ID" --home "$HOME_DIR"
echo -e "${GREEN}Done!${NC}"

echo ""
echo -e "${BLUE}Step 4: Creating pre-mine account...${NC}"
atoshid keys add preminer --keyring-backend "$KEYRING_BACKEND" --home "$HOME_DIR"

# Get the address
PREMINER_ADDRESS=$(atoshid keys show preminer -a --keyring-backend "$KEYRING_BACKEND" --home "$HOME_DIR")
echo -e "${GREEN}Pre-miner address: $PREMINER_ADDRESS${NC}"

echo ""
echo -e "${BLUE}Step 5: Adding genesis account with pre-mined tokens...${NC}"
atoshid genesis add-genesis-account "$PREMINER_ADDRESS" "$PREMINED_AMOUNT" --home "$HOME_DIR"
echo -e "${GREEN}Done!${NC}"

echo ""
echo -e "${BLUE}Step 6: Updating genesis parameters...${NC}"

GENESIS_FILE="$HOME_DIR/config/genesis.json"

# Update staking params
jq '.app_state.staking.params.bond_denom = "aatos"' "$GENESIS_FILE" > tmp.json && mv tmp.json "$GENESIS_FILE"

# Update crisis params
jq '.app_state.crisis.constant_fee.denom = "aatos"' "$GENESIS_FILE" > tmp.json && mv tmp.json "$GENESIS_FILE"

# Update gov params
jq '.app_state.gov.params.min_deposit[0].denom = "aatos"' "$GENESIS_FILE" > tmp.json && mv tmp.json "$GENESIS_FILE"

# Update evm params
jq '.app_state.evm.params.evm_denom = "aatos"' "$GENESIS_FILE" > tmp.json && mv tmp.json "$GENESIS_FILE"

# Update inflation params
jq '.app_state.inflation.params.mint_denom = "aatos"' "$GENESIS_FILE" > tmp.json && mv tmp.json "$GENESIS_FILE"

echo -e "${GREEN}Genesis parameters updated!${NC}"

echo ""
echo -e "${BLUE}═══════════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}=== Genesis Initialization Complete ===${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════════════════${NC}"
echo ""
echo -e "${YELLOW}Pre-miner Account:${NC}"
echo "  Address: $PREMINER_ADDRESS"
echo "  Balance: 70,000,000,000 ATOS"
echo ""
echo -e "${YELLOW}⚠️  IMPORTANT: Save the mnemonic phrase shown above!${NC}"
echo ""
echo -e "${BLUE}Next steps to start a validator:${NC}"
echo ""
echo "  1. Create a validator key:"
echo "     atoshid keys add validator --keyring-backend $KEYRING_BACKEND --home $HOME_DIR"
echo ""
echo "  2. Fund the validator account:"
echo "     (Transfer tokens from preminer account)"
echo ""
echo "  3. Generate gentx:"
echo "     atoshid genesis gentx validator <stake-amount>aatos \\"
echo "       --chain-id $CHAIN_ID \\"
echo "       --keyring-backend $KEYRING_BACKEND \\"
echo "       --home $HOME_DIR"
echo ""
echo "  4. Collect gentxs:"
echo "     atoshid genesis collect-gentxs --home $HOME_DIR"
echo ""
echo "  5. Validate genesis:"
echo "     atoshid genesis validate-genesis --home $HOME_DIR"
echo ""
echo "  6. Start the node:"
echo "     atoshid start --home $HOME_DIR"
echo ""

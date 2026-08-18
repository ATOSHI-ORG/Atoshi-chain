#!/bin/bash

# ============================================================================
# Atoshi Chain - Local Development Node Startup Script
# ============================================================================
# This script initializes and starts a single-node Atoshi chain for development
# and testing purposes.
#
# Chain ID: atoshi_88388-1 (Development/Testing)
# ============================================================================

# Development Chain ID (also used for testing)
CHAINID="${CHAIN_ID:-atoshi_88388-1}"
BASE_DENOM="liao"
DISPLAY_DENOM="atos"
MONIKER="atoshi-dev-node"

# Keyring configuration
# Using 'file' backend for password-protected key storage
# This is more secure than 'test' which allows anyone to export keys without password
KEYRING="file"
KEYALGO="eth_secp256k1"
LOGLEVEL="info"

# Set dedicated home directory for the atoshid instance
HOMEDIR="$HOME/.atoshid-dev"

# EVM tracing (uncomment to enable)
# TRACE="--trace"
TRACE=""

# Feemarket params basefee
BASEFEE=1000000000

# Path variables
CONFIG=$HOMEDIR/config/config.toml
APP_TOML=$HOMEDIR/config/app.toml
GENESIS=$HOMEDIR/config/genesis.json
TMP_GENESIS=$HOMEDIR/config/tmp_genesis.json

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

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
echo "║           Privacy-Preserving EVM Blockchain                       ║"
echo "║                                                                   ║"
echo "╚═══════════════════════════════════════════════════════════════════╝"
echo -e "${NC}"

echo -e "${GREEN}Chain ID:${NC} $CHAINID"
echo -e "${GREEN}Base Denom:${NC} $BASE_DENOM"
echo -e "${GREEN}Display Denom:${NC} $DISPLAY_DENOM"
echo -e "${GREEN}Home Directory:${NC} $HOMEDIR"
echo ""

# Validate dependencies are installed
command -v jq >/dev/null 2>&1 || {
	echo -e "${RED}Error: jq not installed. Please install jq first.${NC}"
	echo "  macOS: brew install jq"
	echo "  Ubuntu: sudo apt-get install jq"
	exit 1
}

# Used to exit on first error (any non-zero exit code)
set -e

# Parse input flags
install=true
overwrite=""

while [[ $# -gt 0 ]]; do
	key="$1"
	case $key in
	-y)
		echo -e "${YELLOW}Flag -y passed -> Overwriting the previous chain data.${NC}"
		overwrite="y"
		shift
		;;
	-n)
		echo -e "${YELLOW}Flag -n passed -> Not overwriting the previous chain data.${NC}"
		overwrite="n"
		shift
		;;
	--no-install)
		echo -e "${YELLOW}Flag --no-install passed -> Skipping installation of the atoshid binary.${NC}"
		install=false
		shift
		;;
	-h|--help)
		echo "Usage: ./local_node.sh [OPTIONS]"
		echo ""
		echo "Options:"
		echo "  -y              Overwrite existing chain data without prompting"
		echo "  -n              Keep existing chain data and start node"
		echo "  --no-install    Skip 'make install' step"
		echo "  -h, --help      Show this help message"
		echo ""
		echo "Environment Variables:"
		echo "  CHAIN_ID        Override default chain ID (default: atoshi_88388-1)"
		echo ""
		exit 0
		;;
	*)
		echo -e "${RED}Unknown flag passed: $key -> Exiting script!${NC}"
		exit 1
		;;
	esac
done

if [[ $install == true ]]; then
	echo -e "${BLUE}Building and installing atoshid...${NC}"
	make install
	echo -e "${GREEN}Installation complete!${NC}"
fi

# User prompt if neither -y nor -n was passed as a flag
# and an existing local node configuration is found.
if [[ $overwrite = "" ]]; then
	if [ -d "$HOMEDIR" ]; then
		printf "\n${YELLOW}An existing folder at '%s' was found.${NC}\n" "$HOMEDIR"
		echo "You can choose to delete this folder and start a new local node with new keys from genesis."
		echo "When declined, the existing local node is started."
		echo ""
		echo -e "${BLUE}Overwrite the existing configuration and start a new local node? [y/n]${NC}"
		read -r overwrite
	else
		overwrite="y"
	fi
fi

# Setup local node if overwrite is set to Yes, otherwise skip setup
if [[ $overwrite == "y" || $overwrite == "Y" ]]; then
	echo -e "${BLUE}Setting up new Atoshi development node...${NC}"
	
	# Remove the previous folder
	rm -rf "$HOMEDIR"

	# Set client config
	atoshid config set client chain-id "$CHAINID" --home "$HOMEDIR"
	atoshid config set client keyring-backend "$KEYRING" --home "$HOMEDIR"

	# ============================================================================
	# Development Accounts
	# ============================================================================
	# These are pre-funded accounts for development and testing purposes.
	# DO NOT use these mnemonics in production!
	# ============================================================================

	# Validator Key (Primary validator account)
	VAL_KEY="validator"
	VAL_MNEMONIC="gesture inject test cycle original hollow east ridge hen combine junk child bacon zero hope comfort vacuum milk pitch cage oppose unhappy lunar seat"

	# Developer accounts for testing
	USER1_KEY="dev0"
	USER1_MNEMONIC="copper push brief egg scan entry inform record adjust fossil boss egg comic alien upon aspect dry avoid interest fury window hint race symptom"

	USER2_KEY="dev1"
	USER2_MNEMONIC="maximum display century economy unlock van census kite error heart snow filter midnight usage egg venture cash kick motor survey drastic edge muffin visual"

	USER3_KEY="dev2"
	USER3_MNEMONIC="will wear settle write dance topic tape sea glory hotel oppose rebel client problem era video gossip glide during yard balance cancel file rose"

	USER4_KEY="dev3"
	USER4_MNEMONIC="doll midnight silk carpet brush boring pluck office gown inquiry duck chief aim exit gain never tennis crime fragile ship cloud surface exotic patch"

	# Import keys from mnemonics
	echo -e "${BLUE}Importing development accounts...${NC}"
	echo ""
	
	echo -e "${GREEN}[1/5] Importing validator account...${NC}"
	echo -e "${YELLOW}Mnemonic: $VAL_MNEMONIC${NC}"
	(echo "$VAL_MNEMONIC"; cat) | atoshid keys add "$VAL_KEY" --recover --keyring-backend "$KEYRING" --algo "$KEYALGO" --home "$HOMEDIR"
	echo ""
	
	echo -e "${GREEN}[2/5] Importing dev0 account...${NC}"
	echo -e "${YELLOW}Mnemonic: $USER1_MNEMONIC${NC}"
	(echo "$USER1_MNEMONIC"; cat) | atoshid keys add "$USER1_KEY" --recover --keyring-backend "$KEYRING" --algo "$KEYALGO" --home "$HOMEDIR"
	echo ""
	
	echo -e "${GREEN}[3/5] Importing dev1 account...${NC}"
	echo -e "${YELLOW}Mnemonic: $USER2_MNEMONIC${NC}"
	(echo "$USER2_MNEMONIC"; cat) | atoshid keys add "$USER2_KEY" --recover --keyring-backend "$KEYRING" --algo "$KEYALGO" --home "$HOMEDIR"
	echo ""
	
	echo -e "${GREEN}[4/5] Importing dev2 account...${NC}"
	echo -e "${YELLOW}Mnemonic: $USER3_MNEMONIC${NC}"
	(echo "$USER3_MNEMONIC"; cat) | atoshid keys add "$USER3_KEY" --recover --keyring-backend "$KEYRING" --algo "$KEYALGO" --home "$HOMEDIR"
	echo ""
	
	echo -e "${GREEN}[5/5] Importing dev3 account...${NC}"
	echo -e "${YELLOW}Mnemonic: $USER4_MNEMONIC${NC}"
	(echo "$USER4_MNEMONIC"; cat) | atoshid keys add "$USER4_KEY" --recover --keyring-backend "$KEYRING" --algo "$KEYALGO" --home "$HOMEDIR"
	echo ""

	# Initialize the chain
	echo -e "${BLUE}Initializing chain...${NC}"
	atoshid init $MONIKER -o --chain-id "$CHAINID" --home "$HOMEDIR"

	# Change parameter token denominations to $BASE_DENOM
	echo -e "${BLUE}Configuring genesis parameters...${NC}"
	jq --arg base_denom "$BASE_DENOM" '.app_state["staking"]["params"]["bond_denom"]=$base_denom' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"
	jq --arg base_denom "$BASE_DENOM" '.app_state["gov"]["deposit_params"]["min_deposit"][0]["denom"]=$base_denom' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"
	jq --arg base_denom "$BASE_DENOM" '.app_state["gov"]["params"]["min_deposit"][0]["denom"]=$base_denom' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"
	jq --arg base_denom "$BASE_DENOM" '.app_state["inflation"]["params"]["mint_denom"]=$base_denom' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"

	# Set gas limit in genesis
	jq '.consensus_params["block"]["max_gas"]="10000000"' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"

	# Set base fee in genesis
	jq '.app_state["feemarket"]["params"]["base_fee"]="'${BASEFEE}'"' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"

	# ========================================================================
	# TEST-MODE parameter overrides for atoshi modules (oracle / tokenomics /
	# energy). DO NOT use these values in production — they collapse the
	# normal 24h / 30-day / 4-year economic windows down to seconds/minutes
	# so a developer can exercise every code path in one afternoon.
	#
	# Production defaults live in each module's DefaultParams() and are not
	# touched here.
	# ========================================================================
	echo -e "${YELLOW}Applying TEST-MODE parameter overrides${NC}"
	echo -e "${YELLOW}  (energy: 60s recover, deploy: 1d; tokenomics: 10-block epoch, 3-day streak, 1k-block halving)${NC}"

	# NOTE: int64 / uint64 fields must be JSON numbers, not strings.
	# math.Int / math.LegacyDec fields (e.g. miner_pool_total) DO require
	# strings because they are encoded with gogoproto.customtype — but we
	# don't override those here. Don't add quotes around the values below.

	# --- ENERGY: 60-second TxEnergy refill window (was 86400) ---
	jq '.app_state.energy.params.tx_energy_max_accrue_window = 60' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"
	# --- ENERGY: DeployEnergy refill in 1 day at threshold holding (was 10) ---
	jq '.app_state.energy.params.deploy_recover_days = 1' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"

	# --- TOKENOMICS: price-check epoch every 10 blocks (was 17280, ~24h) ---
	jq '.app_state.tokenomics.params.price_check_epoch_blocks = 10' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"
	# --- TOKENOMICS: 3 epochs to trigger release (was 30) ---
	jq '.app_state.tokenomics.params.consecutive_days_required = 3' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"
	# --- TOKENOMICS: halve block reward every 1000 blocks (was 25_228_800) ---
	jq '.app_state.tokenomics.params.halving_interval_blocks = 1000' "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"

	# --- ORACLE: whitelist the dev validator as an authorized feeder ---
	# This lets MsgReportPrice succeed straight out of the box for testing.
	# In production, governance / multisig adds approved feeder addresses.
	VAL_ADDR="$(atoshid keys show "$VAL_KEY" -a --keyring-backend "$KEYRING" --home "$HOMEDIR")"
	jq --arg feeder "$VAL_ADDR" \
	   '.app_state.oracle.params.allowed_feeders = [$feeder]' \
	   "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"

	# --- TOKENOMICS: project_treasury_address = first validator's account ---
	# Per design: project pool releases go to the genesis validator address.
	jq --arg treasury "$VAL_ADDR" \
	   '.app_state.tokenomics.params.project_treasury_address = $treasury' \
	   "$GENESIS" >"$TMP_GENESIS" && mv "$TMP_GENESIS" "$GENESIS"

	# Enable prometheus metrics and all APIs for dev node
	if [[ "$OSTYPE" == "darwin"* ]]; then
		sed -i '' 's/prometheus = false/prometheus = true/' "$CONFIG"
		sed -i '' 's/prometheus-retention-time = 0/prometheus-retention-time  = 1000000000000/g' "$APP_TOML"
		sed -i '' 's/enabled = false/enabled = true/g' "$APP_TOML"
		sed -i '' 's/enable = false/enable = true/g' "$APP_TOML"
		# Don't enable Rosetta API by default
		grep -q -F '[rosetta]' "$APP_TOML" && sed -i '' '/\[rosetta\]/,/^\[/ s/enable = true/enable = false/' "$APP_TOML"
		# Don't enable memiavl by default
		grep -q -F '[memiavl]' "$APP_TOML" && sed -i '' '/\[memiavl\]/,/^\[/ s/enable = true/enable = false/' "$APP_TOML"
		# Don't enable versionDB by default
		grep -q -F '[versiondb]' "$APP_TOML" && sed -i '' '/\[versiondb\]/,/^\[/ s/enable = true/enable = false/' "$APP_TOML"
	else
		sed -i 's/prometheus = false/prometheus = true/' "$CONFIG"
		sed -i 's/prometheus-retention-time  = "0"/prometheus-retention-time  = "1000000000000"/g' "$APP_TOML"
		sed -i 's/enabled = false/enabled = true/g' "$APP_TOML"
		sed -i 's/enable = false/enable = true/g' "$APP_TOML"
		# Don't enable Rosetta API by default
		grep -q -F '[rosetta]' "$APP_TOML" && sed -i '/\[rosetta\]/,/^\[/ s/enable = true/enable = false/' "$APP_TOML"
		# Don't enable memiavl by default
		grep -q -F '[memiavl]' "$APP_TOML" && sed -i '/\[memiavl\]/,/^\[/ s/enable = true/enable = false/' "$APP_TOML"
		# Don't enable versionDB by default
		grep -q -F '[versiondb]' "$APP_TOML" && sed -i '/\[versiondb\]/,/^\[/ s/enable = true/enable = false/' "$APP_TOML"
	fi

	# Change proposal periods to pass within a reasonable time for local testing
	sed -i.bak 's/"max_deposit_period": "172800s"/"max_deposit_period": "30s"/g' "$GENESIS"
	sed -i.bak 's/"voting_period": "172800s"/"voting_period": "30s"/g' "$GENESIS"
	sed -i.bak 's/"expedited_voting_period": "86400s"/"expedited_voting_period": "15s"/g' "$GENESIS"

	# Set custom pruning settings for development
	# For dev environment, we use "nothing" to keep all historical states
	# This allows querying any historical block state (required for MetaMask and debugging)
	# Options: "default", "nothing", "everything", "custom"
	#   - nothing: keep all states (best for development)
	#   - everything: prune all states except current (smallest disk usage)
	#   - default: keep last 100 states
	#   - custom: configure pruning-keep-recent and pruning-interval
	if [[ "$OSTYPE" == "darwin"* ]]; then
		sed -i '' 's/pruning = "default"/pruning = "nothing"/g' "$APP_TOML"
	else
		sed -i 's/pruning = "default"/pruning = "nothing"/g' "$APP_TOML"
	fi

	# Allocate genesis accounts (cosmos formatted addresses)
	# Validator: 100,000,000 ATOS
	# Dev accounts: 1,000 ATOS each
	echo -e "${BLUE}Allocating genesis accounts...${NC}"
	atoshid add-genesis-account "$(atoshid keys show "$VAL_KEY" -a --keyring-backend "$KEYRING" --home "$HOMEDIR")" 100000000000000000000000000$BASE_DENOM --keyring-backend "$KEYRING" --home "$HOMEDIR"
	atoshid add-genesis-account "$(atoshid keys show "$USER1_KEY" -a --keyring-backend "$KEYRING" --home "$HOMEDIR")" 1000000000000000000000$BASE_DENOM --keyring-backend "$KEYRING" --home "$HOMEDIR"
	atoshid add-genesis-account "$(atoshid keys show "$USER2_KEY" -a --keyring-backend "$KEYRING" --home "$HOMEDIR")" 1000000000000000000000$BASE_DENOM --keyring-backend "$KEYRING" --home "$HOMEDIR"
	atoshid add-genesis-account "$(atoshid keys show "$USER3_KEY" -a --keyring-backend "$KEYRING" --home "$HOMEDIR")" 1000000000000000000000$BASE_DENOM --keyring-backend "$KEYRING" --home "$HOMEDIR"
	atoshid add-genesis-account "$(atoshid keys show "$USER4_KEY" -a --keyring-backend "$KEYRING" --home "$HOMEDIR")" 1000000000000000000000$BASE_DENOM --keyring-backend "$KEYRING" --home "$HOMEDIR"

	# Sign genesis transaction
	echo -e "${BLUE}Creating genesis transaction...${NC}"
	atoshid gentx "$VAL_KEY" 1000000000000000000000$BASE_DENOM --gas-prices ${BASEFEE}$BASE_DENOM --keyring-backend "$KEYRING" --chain-id "$CHAINID" --home "$HOMEDIR"

	# Collect genesis tx
	atoshid collect-gentxs --home "$HOMEDIR"

	# Run this to ensure everything worked and that the genesis file is setup correctly
	atoshid validate-genesis --home "$HOMEDIR"

	echo -e "${GREEN}Genesis setup complete!${NC}"
fi

# Print account information
echo ""
echo -e "${BLUE}═══════════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}Development Accounts:${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════════════════${NC}"
atoshid keys list --keyring-backend "$KEYRING" --home "$HOMEDIR"
echo -e "${BLUE}═══════════════════════════════════════════════════════════════════${NC}"
echo ""
echo -e "${GREEN}RPC Endpoints:${NC}"
echo "  Tendermint RPC: http://localhost:26657"
echo "  EVM JSON-RPC:   http://localhost:8545"
echo "  WebSocket:      ws://localhost:8546"
echo "  REST API:       http://localhost:1317"
echo "  gRPC:           localhost:9090"
echo ""
echo -e "${BLUE}═══════════════════════════════════════════════════════════════════${NC}"
echo -e "${GREEN}Starting Atoshi node...${NC}"
echo -e "${BLUE}═══════════════════════════════════════════════════════════════════${NC}"

# Start the node
atoshid start \
	--metrics "$TRACE" \
	--log_level $LOGLEVEL \
	--minimum-gas-prices=0.0001$BASE_DENOM \
	--json-rpc.api eth,txpool,personal,net,debug,web3 \
	--home "$HOMEDIR" \
	--chain-id "$CHAINID"

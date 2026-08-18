# Atoshi Chain

<p align="center">
  <img src="docs/atoshi-logo.png" alt="Atoshi Logo" width="200"/>
</p>

<h3 align="center">Privacy-Preserving EVM Blockchain</h3>

<p align="center">
  <a href="https://github.com/ATOSHI-ORG/Atoshi-chain/releases">
    <img src="https://img.shields.io/github/v/release/ATOSHI-ORG/Atoshi-chain?style=flat-square" alt="Release">
  </a>
  <a href="https://github.com/ATOSHI-ORG/Atoshi-chain/blob/main/LICENSE">
    <img src="https://img.shields.io/badge/license-ENCL--1.0-blue?style=flat-square" alt="License">
  </a>
</p>

<p align="center">
  <a href="https://www.atoshi.org">
    <img src="https://img.shields.io/badge/Website-atoshi.org-2962FF?style=flat-square&logo=google-chrome&logoColor=white" alt="Website">
  </a>
  <a href="https://x.com/atoshiofficial">
    <img src="https://img.shields.io/badge/Follow-@atoshiofficial-000000?style=flat-square&logo=x&logoColor=white" alt="X (Twitter)">
  </a>
  <a href="https://t.me/atoshiofficial">
    <img src="https://img.shields.io/badge/Telegram-@atoshiofficial-26A5E4?style=flat-square&logo=telegram&logoColor=white" alt="Telegram">
  </a>
</p>

---

## 🌟 Overview

**Atoshi** is a privacy-preserving EVM-compatible blockchain designed to enable confidential transactions while maintaining full compatibility with existing Ethereum tooling, wallets, and smart contracts.

### Key Features

- 🔐 **Privacy Transactions**: Hide transaction amounts, sender/receiver identities using Zero-Knowledge Proofs
- ⚡ **EVM Compatible**: Full compatibility with Ethereum smart contracts, wallets (MetaMask), and development tools
- 🛡️ **Shield Contracts**: Deposit-withdraw privacy pools for confidential asset transfers
- 🌐 **Multi-Asset Privacy**: Support for native tokens, ERC-20, NFTs, and SBTs
- 🔗 **IBC Enabled**: Cross-chain communication with Cosmos ecosystem

---

## 📋 Chain Specifications

| Property | Value |
|----------|-------|
| **Chain Name** | Atoshi |
| **Native Token** | ATOSHI (ATOS) |
| **Base Denom** | `liao` |
| **Display Denom** | `atos` |
| **Decimals** | 18 |
| **Total Supply** | 10,000,000,000,000 ATOS (10 Trillion) |
| **Pre-mine** | 70,000,000,000 ATOS (70 Billion) |

### Chain IDs

| Network | Chain ID | EIP-155 ID |
|---------|----------|------------|
| **Mainnet** | `atoshi_88188-1` | 88188 |
| **Testnet** | `atoshi_88288-1` | 88288 |
| **Devnet** | `atoshi_88388-1` | 88388 |

### Bech32 Prefixes

| Type | Prefix |
|------|--------|
| Account Address | `atoshi` |
| Validator Operator | `atoshivaloper` |
| Consensus Node | `atoshivalcons` |

---

## 🏗️ Architecture

### Privacy Transaction System

Atoshi implements a comprehensive privacy layer using Zero-Knowledge Proofs (ZK-SNARKs/STARKs) to enable confidential transactions on the EVM.

```
┌─────────────────┐     ┌─────────────────────────┐     ┌─────────────────────┐
│   User Layer    │     │    L2 Privacy Layer     │     │  L1 Settlement      │
├─────────────────┤     ├─────────────────────────┤     ├─────────────────────┤
│                 │     │                         │     │                     │
│  User Wallet    │     │  Sequencer/Relayer      │     │  Rollup Contract    │
│       │         │     │         │               │     │        │            │
│       ▼         │     │         ▼               │     │        ▼            │
│  Generate       │     │  Transaction Pool       │     │  Verifier Contract  │
│  Stealth Addr   │────▶│         │               │────▶│        │            │
│       │         │     │         ▼               │     │        ▼            │
│       ▼         │     │  Batch Builder          │     │  State Storage      │
│  Create Note    │     │         │               │     │                     │
│  Commitment     │     │         ▼               │     └─────────────────────┘
│       │         │     │  Prover Service         │
│       ▼         │     │         │               │
│  User Wallet    │     │         ▼               │
│                 │     │  Batch Proof            │
└─────────────────┘     │         │               │
                        │         ▼               │
                        │  State Tree Manager     │
                        │                         │
                        └─────────────────────────┘
```

### Core Components

#### 1. Shield Contract (L1)
- Receives deposit commitments
- Verifies withdrawal ZK proofs
- Manages nullifier registry
- Executes asset transfers

#### 2. Commitment System
- Off-chain generation of secret data (amount, recipient pubkey, randomness)
- Hash commitment submitted to Shield contract
- Acts as a "receipt" for deposited funds

#### 3. Merkle Tree
- Off-chain tree structure for all deposits
- Tree root stored on-chain
- Membership proofs required for withdrawals

#### 4. Nullifier
- Prevents double-spending
- One-time "seal" computed in ZK circuit
- Recorded on-chain after successful withdrawal

#### 5. ZK Proof System
- Generated off-chain by Prover
- Verified on-chain by Shield contract
- Supports Plonky2/Halo2 proving systems

### Transaction Flow

#### Deposit Flow
```
1. User generates commitment (hash of secret data) locally
2. User submits commitment to Shield contract
3. Contract adds commitment to pending queue
4. Commitment is added to off-chain Merkle tree
5. Tree root is updated on-chain
```

#### Withdrawal Flow
```
1. User runs Prover locally with:
   - Secret data (amount, randomness, etc.)
   - Merkle proof of membership
2. Prover generates ZK Proof + Nullifier
3. User submits to Shield contract:
   - ZK Proof
   - Nullifier
   - Recipient address
4. Contract verifies:
   - Proof validity
   - Nullifier not used
5. Contract executes transfer and records nullifier
```

---

## 🚀 Quick Start

### Prerequisites

- Go 1.22+
- Make
- jq

### Build from Source

```bash
# Clone the repository
git clone https://github.com/ATOSHI-ORG/Atoshi-chain.git
cd atoshi

# Build and install
make install

# Verify installation
atoshid version
```

### Run Local Development Node

```bash
# Start a local single-node chain (development/testing)
./local_node.sh

# Options:
#   -y              Overwrite existing data without prompting
#   -n              Keep existing data and start node
#   --no-install    Skip the build step
#   -h, --help      Show help message
```

### Development Endpoints

After starting the local node:

| Service | Endpoint |
|---------|----------|
| Tendermint RPC | http://localhost:26657 |
| EVM JSON-RPC | http://localhost:8545 |
| WebSocket | ws://localhost:8546 |
| REST API | http://localhost:1317 |
| gRPC | localhost:9090 |

### Connect MetaMask

1. Open MetaMask → Settings → Networks → Add Network
2. Configure:
   - **Network Name**: Atoshi Devnet
   - **RPC URL**: http://localhost:8545
   - **Chain ID**: 88388
   - **Currency Symbol**: ATOS
   - **Block Explorer**: (leave empty for local)

---

## 📁 Project Structure

```
atoshi/
├── app/                    # Application configuration
│   ├── app.go              # Main application setup
│   ├── config.go           # Chain configuration
│   └── ante/               # Transaction ante handlers
├── cmd/
│   └── atoshid/            # CLI binary
│       ├── main.go
│       └── root.go
├── contracts/              # Solidity contracts
├── crypto/                 # Cryptographic utilities
├── precompiles/            # EVM precompiled contracts
├── proto/                  # Protobuf definitions
├── rpc/                    # JSON-RPC implementation
├── scripts/                # Utility scripts
│   └── init_genesis.sh     # Genesis initialization
├── server/                 # Server configuration
├── types/                  # Core types
├── utils/                  # Utility functions
├── x/                      # Cosmos SDK modules
│   ├── erc20/              # ERC20 module
│   ├── evm/                # EVM module
│   ├── feemarket/          # Fee market (EIP-1559)
│   ├── inflation/          # Token inflation
│   └── vesting/            # Vesting accounts
├── local_node.sh           # Local development script
├── Makefile                # Build commands
└── README.md
```

---

## 🔧 Configuration

### Genesis Configuration

Initialize a new chain with pre-mined tokens:

```bash
# Using the initialization script
./scripts/init_genesis.sh

# Or manually:
atoshid init my-node --chain-id atoshi_88188-1
atoshid keys add preminer
atoshid genesis add-genesis-account <address> 70000000000000000000000000000liao
```

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `CHAIN_ID` | Chain identifier | `atoshi_88388-1` |
| `MONIKER` | Node moniker | `atoshi-node` |
| `KEYRING_BACKEND` | Keyring backend | `test` |
| `HOME_DIR` | Data directory | `~/.atoshid` |

---

## 🛠️ Development

### Build Commands

```bash
# Build binary
make build

# Install to GOPATH
make install

# Run tests
make test

# Generate protobuf
make proto-gen

# Lint code
make lint
```

### Testing

```bash
# Run all tests
make test

# Run specific package tests
go test ./x/evm/...

# Run with coverage
go test -cover ./...
```

---

## 🔐 Privacy Technology Stack

### Zero-Knowledge Proofs
- **Proving System**: Plonky2 / Halo2
- **Hash Function**: Poseidon (ZK-friendly)
- **Commitment Scheme**: Pedersen commitments

### Supported Privacy Features

| Feature | Status |
|---------|--------|
| Amount Hiding | ✅ Planned |
| Sender/Receiver Hiding | ✅ Planned |
| Multi-Asset Privacy | ✅ Planned |
| ERC-20 Privacy | ✅ Planned |
| NFT Privacy | 🔄 In Progress |
| SBT Privacy | 🔄 In Progress |

---

## 📚 Documentation

- [Technical Whitepaper](docs/whitepaper.md)
- [Privacy Protocol Specification](docs/privacy-spec.md)
- [API Reference](docs/api.md)
- [Smart Contract Guide](docs/contracts.md)

---

## 🤝 Contributing

We welcome contributions! Please see our [Contributing Guide](CONTRIBUTING.md) for details.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

---

## 📄 License

This project is licensed under the [ENCL-1.0 License](LICENSE).

---

## 🔗 Links

- [Website](https://atoshi.network)
- [Documentation](https://docs.atoshi.network)
- [Block Explorer](https://explorer.atoshi.network)
- [Discord](https://discord.gg/atoshi)
- [Twitter](https://twitter.com/atoshi_chain)

---

<p align="center">
  <b>Built with ❤️ by the Atoshi Team</b>
</p>

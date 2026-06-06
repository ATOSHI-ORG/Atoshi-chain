// migration-merkle builds the Merkle snapshot for the Atoshi pre-mine migration
// airdrop. It reads a CSV (claimer_bech32,amount_uatos) and produces:
//   - <out>/root.txt     hex-encoded SHA-256 Merkle root (set as
//                        Params.MigrationMerkleRoot via gov MsgUpdateParams)
//   - <out>/proofs.json  per-claimer proofs ready to feed into MsgClaimMigrationTokens
//
// Hash convention matches x/tokenomics/keeper.verifyMerkleClaim:
//   payload = uvarint(len(claimer)) || claimer || uvarint(len(amount)) || amount
//   leaf    = sha256(sha256(payload))           // length-prefixed, double-hashed
//   parent  = sha256(min(a,b) || max(a,b))      // sorted-pair, OpenZeppelin-style
//
// Trees with an odd number of nodes at any level promote the unmatched node
// unchanged to the next level (no duplication), which keeps proofs short and
// matches the verifier's pair-sort behavior.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type entry struct {
	Claimer string `json:"claimer"`
	Amount  string `json:"amount"`
}

type proofEntry struct {
	Claimer string   `json:"claimer"`
	Amount  string   `json:"amount"`
	Leaf    string   `json:"leaf"`
	Proof   []string `json:"proof"`
}

type output struct {
	Root   string       `json:"root"`
	Count  int          `json:"count"`
	Proofs []proofEntry `json:"proofs"`
}

func main() {
	in := flag.String("in", "", "input CSV (claimer,amount). '-' reads stdin.")
	out := flag.String("out", "./migration", "output directory")
	flag.Parse()

	if *in == "" {
		fmt.Fprintln(os.Stderr, "usage: migration-merkle -in snapshot.csv -out ./migration")
		os.Exit(2)
	}

	entries, err := readCSV(*in)
	if err != nil {
		die(err)
	}
	if len(entries) == 0 {
		die(fmt.Errorf("snapshot is empty"))
	}

	leaves := make([][]byte, len(entries))
	for i, e := range entries {
		leaves[i] = leafHash(e.Claimer, e.Amount)
	}

	root, levels := buildTree(leaves)

	proofs := make([]proofEntry, len(entries))
	for i, e := range entries {
		proof := proofFor(i, levels)
		hexed := make([]string, len(proof))
		for j, p := range proof {
			hexed[j] = hex.EncodeToString(p)
		}
		proofs[i] = proofEntry{
			Claimer: e.Claimer,
			Amount:  e.Amount,
			Leaf:    hex.EncodeToString(leaves[i]),
			Proof:   hexed,
		}
	}

	if err := os.MkdirAll(*out, 0o755); err != nil {
		die(err)
	}
	rootHex := hex.EncodeToString(root)
	if err := os.WriteFile(filepath.Join(*out, "root.txt"), []byte(rootHex+"\n"), 0o644); err != nil {
		die(err)
	}
	f, err := os.Create(filepath.Join(*out, "proofs.json"))
	if err != nil {
		die(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output{Root: rootHex, Count: len(entries), Proofs: proofs}); err != nil {
		die(err)
	}

	fmt.Printf("wrote merkle root: %s\n", rootHex)
	fmt.Printf("entries: %d  output: %s\n", len(entries), *out)
}

func readCSV(path string) ([]entry, error) {
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	rows, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}
	out := make([]entry, 0, len(rows))
	for i, row := range rows {
		if len(row) < 2 {
			continue
		}
		claimer := strings.TrimSpace(row[0])
		amount := strings.TrimSpace(row[1])
		if claimer == "" || amount == "" {
			continue
		}
		// Skip header row if present.
		if i == 0 && (strings.EqualFold(claimer, "claimer") || strings.EqualFold(claimer, "address")) {
			continue
		}
		out = append(out, entry{Claimer: claimer, Amount: amount})
	}
	return out, nil
}

// buildTree returns the root and the per-level node lists (level 0 = leaves).
func buildTree(leaves [][]byte) ([]byte, [][][]byte) {
	if len(leaves) == 1 {
		return leaves[0], [][][]byte{leaves}
	}
	levels := [][][]byte{leaves}
	current := leaves
	for len(current) > 1 {
		next := make([][]byte, 0, (len(current)+1)/2)
		for i := 0; i < len(current); i += 2 {
			if i+1 == len(current) {
				// Odd node: promote unchanged.
				next = append(next, current[i])
				continue
			}
			next = append(next, hashPair(current[i], current[i+1]))
		}
		levels = append(levels, next)
		current = next
	}
	return current[0], levels
}

func proofFor(index int, levels [][][]byte) [][]byte {
	proof := make([][]byte, 0, len(levels))
	idx := index
	for level := 0; level < len(levels)-1; level++ {
		nodes := levels[level]
		var sibling int
		if idx%2 == 0 {
			sibling = idx + 1
		} else {
			sibling = idx - 1
		}
		if sibling < len(nodes) {
			proof = append(proof, nodes[sibling])
		}
		// If sibling is out of range, this node was the odd one promoted.
		idx /= 2
	}
	return proof
}

// leafHash matches x/tokenomics/keeper.migrationLeafHash exactly.
func leafHash(claimer, amount string) []byte {
	var buf []byte
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(claimer)))
	buf = append(buf, lenBuf[:n]...)
	buf = append(buf, claimer...)
	n = binary.PutUvarint(lenBuf[:], uint64(len(amount)))
	buf = append(buf, lenBuf[:n]...)
	buf = append(buf, amount...)
	inner := sha256.Sum256(buf)
	leaf := sha256.Sum256(inner[:])
	return leaf[:]
}

func hashPair(a, b []byte) []byte {
	combined := make([]byte, 0, len(a)+len(b))
	if bytes.Compare(a, b) <= 0 {
		combined = append(combined, a...)
		combined = append(combined, b...)
	} else {
		combined = append(combined, b...)
		combined = append(combined, a...)
	}
	h := sha256.Sum256(combined)
	return h[:]
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

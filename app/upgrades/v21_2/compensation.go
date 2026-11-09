package v21_2

import (
	_ "embed"
	"encoding/csv"
	"fmt"
	"strings"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
)

// compensationCSV is the payout list as produced on 2026-10-20 by
// docs/slash-2026-10-18/generate.py, embedded verbatim.
//
// Embedded rather than transcribed into Go so the artifact the chain pays from
// is byte-identical to the one operators and auditors already hold. Columns:
//
//	Cosmos地址, 以太坊地址, 質押節點(状态)与损失, 应补偿ATOS, 备注
//
// Only the first and fourth are read. The Ethereum address is informational --
// these are Cosmos-side delegations and pay out on this chain.
//
//go:embed slash_compensation.csv
var compensationCSV string

// utf8BOM is what generate.py's csv writer puts at the head of the file.
// Written as byte escapes: a literal U+FEFF in Go source is a compile error
// anywhere but the first character.
const utf8BOM = "\xef\xbb\xbf"

// Payout is one line of the compensation list.
type Payout struct {
	Address sdk.AccAddress
	Amount  math.Int // base units

	// Bech32 is the address exactly as the list spells it.
	//
	// Kept because AccAddress.String() re-encodes through the process-wide
	// sdk.Config, so it is only the address in the list if that config happens
	// to be set to this chain's prefix. For a log line naming who was paid, and
	// for any comparison back against the list, the original string is the
	// trustworthy one.
	Bech32 string
}

// expectedRows and expectedTotal are what the embedded file must contain.
//
// Asserted rather than merely computed, because this list is the one input to
// the step that mints. A truncated read, a re-export with different rounding
// or a hand edit all show up as a parse failure here instead of as a wrong
// amount of new supply -- and a wrong amount of new supply is exactly the thing
// no later proposal can undo.
const (
	expectedRows  = 1843
	expectedTotal = "41965283235217000000000000" // 41,965,283.235217 ATOS
)

// bech32Prefix is cmd/config.Bech32Prefix, repeated rather than imported so
// this package does not pull the CLI config in.
const bech32Prefix = "atoshi"

// decodeAtoshiAddress parses one recipient, checking the prefix itself rather
// than through sdk.AccAddressFromBech32.
//
// AccAddressFromBech32 compares against the process-wide sdk.Config, which is
// set by an init in the binary that happens to be running. A step that mints
// supply should not read a recipient list through mutable global state: if the
// prefix were ever wrong or unset, every row would decode to the wrong account
// or none would decode at all, and this file is the last place either would be
// noticed. The expected prefix for this list is known and fixed, so it is
// stated.
func decodeAtoshiAddress(s string) (sdk.AccAddress, error) {
	hrp, bz, err := bech32.DecodeAndConvert(s)
	if err != nil {
		return nil, err
	}
	if hrp != bech32Prefix {
		return nil, fmt.Errorf("expected the %q prefix, got %q", bech32Prefix, hrp)
	}
	if len(bz) != 20 {
		return nil, fmt.Errorf("expected a 20-byte account address, got %d bytes", len(bz))
	}
	return sdk.AccAddress(bz), nil
}

// LoadPayouts parses the embedded list and checks it against the figures above.
//
// Returns the rows to pay and their total. With CompensateOwnSelfDelegations
// false, our two self-delegation rows are dropped after the whole-file
// assertions pass, so the file is still verified in full either way.
func LoadPayouts() ([]Payout, math.Int, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(compensationCSV, utf8BOM)))
	r.FieldsPerRecord = 5

	records, err := r.ReadAll()
	if err != nil {
		return nil, math.ZeroInt(), fmt.Errorf("compensation list is unreadable: %w", err)
	}
	if len(records) == 0 {
		return nil, math.ZeroInt(), fmt.Errorf("compensation list is empty")
	}
	rows := records[1:] // drop the header

	if len(rows) != expectedRows {
		return nil, math.ZeroInt(), fmt.Errorf(
			"compensation list has %d rows, expected %d", len(rows), expectedRows)
	}

	payouts := make([]Payout, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	fileTotal := math.ZeroInt()
	payTotal := math.ZeroInt()

	for i, row := range rows {
		bech := strings.TrimSpace(row[0])
		addr, err := decodeAtoshiAddress(bech)
		if err != nil {
			return nil, math.ZeroInt(), fmt.Errorf(
				"compensation row %d has an invalid address %q: %w", i+2, bech, err)
		}
		// A duplicate would be paid twice, and the total assertion would still
		// pass if the file were generated with one, so it has to be caught here.
		if _, dup := seen[bech]; dup {
			return nil, math.ZeroInt(), fmt.Errorf(
				"compensation row %d repeats address %s", i+2, bech)
		}
		seen[bech] = struct{}{}

		// The column is whole ATOS with six decimals. LegacyDec carries
		// eighteen, so the scale to base units is exact -- no rounding mode to
		// pick and nothing to truncate.
		dec, err := math.LegacyNewDecFromStr(strings.TrimSpace(row[3]))
		if err != nil {
			return nil, math.ZeroInt(), fmt.Errorf(
				"compensation row %d has an unparseable amount %q: %w", i+2, row[3], err)
		}
		if !dec.IsPositive() {
			return nil, math.ZeroInt(), fmt.Errorf(
				"compensation row %d has a non-positive amount %s", i+2, dec)
		}
		amount := dec.MulInt(math.NewIntWithDecimal(1, 18)).TruncateInt()

		fileTotal = fileTotal.Add(amount)

		if _, own := OwnSelfDelegations[bech]; own && !CompensateOwnSelfDelegations {
			continue
		}
		payouts = append(payouts, Payout{Address: addr, Amount: amount, Bech32: bech})
		payTotal = payTotal.Add(amount)
	}

	if fileTotal.String() != expectedTotal {
		return nil, math.ZeroInt(), fmt.Errorf(
			"compensation list totals %s, expected %s", fileTotal, expectedTotal)
	}

	return payouts, payTotal, nil
}

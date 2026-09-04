// Command hyperlane_announce generates the signature for MsgAnnounceValidator
// offline, so that a validator's storage location can be announced by someone
// else's account.
//
// Why this exists
//
// The Hyperlane agent cannot announce itself on this chain. Its cosmosKey signer
// derives an account with standard Cosmos secp256k1 (ripemd160(sha256(pubkey))),
// while this chain is Ethermint-based and app/ante/sigverify.go accepts only
// ethsecp256k1 public keys -- anything else falls through to
// "unrecognized/unsupported public key type". So the agent's self-announce
// transaction is rejected no matter how well funded that account is, and its
// error ("make sure you have enough funds") points the wrong way.
//
// MsgAnnounceValidator is submittable by anyone: its signer field is `creator`,
// and the validator's authenticity comes from an ECDSA signature carried inside
// the message, recovered and compared against the claimed validator address.
// So we can produce that signature here and submit it with a key the chain does
// accept.
//
// The digest mirrors x/core/01_interchain_security/types.GetAnnouncementDigest
// followed by util.GetEthSigningHash. Each signature is verified through the
// same recovery path the chain uses before being printed -- a signature that
// does not recover to its own address is a silent failure otherwise.
//
// Usage:
//
//	go run ./scripts/hyperlane_announce <mailbox-id> <domain> <key1,key2,...> <loc1,loc2,...>
//
// The locations are what the validator itself would announce:
// "file://" + its HYP_CHECKPOINTSYNCER_PATH.
package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

// 和链上 x/core/01_interchain_security/types.GetAnnouncementDigest 一致：
//   domainHash        = keccak256( be32(domain) || mailbox(32) || "HYPERLANE_ANNOUNCEMENT" )
//   announcementDigest= keccak256( domainHash || storageLocation )
func announcementDigest(storageLocation string, domainID uint32, mailbox []byte) []byte {
	d := make([]byte, 4)
	binary.BigEndian.PutUint32(d, domainID)
	domainHash := crypto.Keccak256(slices.Concat(d, mailbox, []byte("HYPERLANE_ANNOUNCEMENT")))
	return crypto.Keccak256(slices.Concat(domainHash, []byte(storageLocation)))
}

// util.GetEthSigningHash：EIP-191 personal_sign 前缀
func ethSigningHash(msg []byte) []byte {
	prefix := fmt.Sprintf("\x19Ethereum Signed Message:\n%v", len(msg))
	return crypto.Keccak256(slices.Concat([]byte(prefix), msg))
}

func main() {
	mailboxHex := os.Args[1]
	domain := uint32(0)
	fmt.Sscanf(os.Args[2], "%d", &domain)
	keys := strings.Split(os.Args[3], ",")
	locations := strings.Split(os.Args[4], ",")

	mailbox, err := hex.DecodeString(strings.TrimPrefix(mailboxHex, "0x"))
	if err != nil || len(mailbox) != 32 {
		panic("mailbox 必须是 32 字节十六进制")
	}

	for i, kh := range keys {
		loc := locations[i]
		priv, err := crypto.HexToECDSA(strings.TrimPrefix(kh, "0x"))
		if err != nil {
			panic(err)
		}
		addr := crypto.PubkeyToAddress(priv.PublicKey)

		digest := announcementDigest(loc, domain, mailbox)
		hash := ethSigningHash(digest)

		sig, err := crypto.Sign(hash, priv)
		if err != nil {
			panic(err)
		}
		// 链上 RecoverEthSignature 会先减 27，所以这里要按 EIP-155 加上
		sig[64] += 27

		// 自检：按链上完全一样的路径把地址恢复出来，对不上就不输出
		check := make([]byte, 65)
		copy(check, sig)
		check[64] -= 27
		pub, err := crypto.SigToPub(hash, check)
		if err != nil || crypto.PubkeyToAddress(*pub) != addr {
			panic(fmt.Sprintf("自检失败 key %d", i+1))
		}

		fmt.Printf("VALIDATOR=%s\nLOCATION=%s\nSIGNATURE=0x%s\n\n",
			strings.ToLower(addr.Hex()), loc, hex.EncodeToString(sig))
	}
}

package reward

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// addr builds a common.Address from a hex string, panicking on bad input.
// Test-only helper — production code uses common.HexToAddress directly.
func addr(t *testing.T, s string) common.Address {
	t.Helper()
	if !common.IsHexAddress(s) {
		t.Fatalf("invalid hex address %q", s)
	}
	return common.HexToAddress(s)
}

// expectedLeaf reproduces the encoding contract by hand so the test catches
// any future drift in EncodeLeaf. It hardcodes the abi.encodePacked layout:
// 20-byte address || 32-byte big-endian uint256.
func expectedLeaf(t *testing.T, a common.Address, amount *big.Int) []byte {
	t.Helper()
	buf := make([]byte, 0, 52)
	buf = append(buf, a.Bytes()...)
	padded := make([]byte, 32)
	if amount != nil && amount.Sign() >= 0 {
		b := amount.Bytes()
		if len(b) > 32 {
			b = b[len(b)-32:]
		}
		copy(padded[32-len(b):], b)
	}
	buf = append(buf, padded...)
	return crypto.Keccak256(buf)
}

// TestEncodeLeaf_Format asserts the abi.encodePacked layout. A regression here
// would silently break every claim — this is the highest-priority guarantee.
func TestEncodeLeaf_Format(t *testing.T) {
	a := addr(t, "0x1111111111111111111111111111111111111111")
	amount := big.NewInt(0x2a) // 42

	got := EncodeLeaf(Leaf{Wallet: a, Amount: amount})
	want := expectedLeaf(t, a, amount)
	if !bytes.Equal(got, want) {
		t.Errorf("EncodeLeaf mismatch\n got:  %x\n want: %x", got, want)
	}
	if len(got) != 32 {
		t.Errorf("EncodeLeaf returned %d bytes, want 32", len(got))
	}
}

// TestEncodeLeaf_NotAbiEncode pins down the abi.encodePacked vs abi.encode
// distinction. abi.encode would 32-pad the address (12 leading zero bytes);
// the resulting hash differs. If someone "fixes" EncodeLeaf to use the wrong
// encoding, this test fails immediately.
func TestEncodeLeaf_NotAbiEncode(t *testing.T) {
	a := addr(t, "0x1111111111111111111111111111111111111111")
	amount := big.NewInt(100)

	got := EncodeLeaf(Leaf{Wallet: a, Amount: amount})

	// Build the abi.encode variant: 32-byte padded address || 32-byte uint256.
	abiEncoded := make([]byte, 0, 64)
	addrPadded := make([]byte, 32)
	copy(addrPadded[12:], a.Bytes())
	abiEncoded = append(abiEncoded, addrPadded...)
	abiEncoded = append(abiEncoded, padUint256(amount)...)
	wrongHash := crypto.Keccak256(abiEncoded)

	if bytes.Equal(got, wrongHash) {
		t.Fatalf("EncodeLeaf appears to use abi.encode (32-padded address) — must be abi.encodePacked")
	}
}

// TestEncodeLeaf_LargeAmount confirms uint256 round-trip. 2^200 doesn't fit in
// int64 — the implementation must not silently downcast.
func TestEncodeLeaf_LargeAmount(t *testing.T) {
	a := addr(t, "0x2222222222222222222222222222222222222222")
	huge := new(big.Int).Lsh(big.NewInt(1), 200) // 2^200

	got := EncodeLeaf(Leaf{Wallet: a, Amount: huge})
	want := expectedLeaf(t, a, huge)
	if !bytes.Equal(got, want) {
		t.Errorf("large-amount mismatch\n got:  %x\n want: %x", got, want)
	}
}

// TestBuildTree_EmptyError: Empty input is a programming error, not a "happy
// zero-leaf root". The on-chain Distributor must reject zero-root cycles per
// ADR-0008 §Decision; reject them here too.
func TestBuildTree_EmptyError(t *testing.T) {
	_, _, err := BuildTree(nil)
	if err == nil {
		t.Fatal("expected error from BuildTree(nil)")
	}
	_, _, err = BuildTree([]Leaf{})
	if err == nil {
		t.Fatal("expected error from BuildTree([]Leaf{})")
	}
}

// TestBuildTree_NegativeAmount: explicit guard so we never feed a malformed
// amount into the tree.
func TestBuildTree_NegativeAmount(t *testing.T) {
	a := addr(t, "0x3333333333333333333333333333333333333333")
	_, _, err := BuildTree([]Leaf{{Wallet: a, Amount: big.NewInt(-1)}})
	if err == nil {
		t.Fatal("expected error for negative amount")
	}
}

// TestBuildTree_NilAmount: nil amounts are rejected explicitly so the cycle
// builder fails noisily on a missing wallet rather than minting a zero leaf.
func TestBuildTree_NilAmount(t *testing.T) {
	a := addr(t, "0x4444444444444444444444444444444444444444")
	_, _, err := BuildTree([]Leaf{{Wallet: a, Amount: nil}})
	if err == nil {
		t.Fatal("expected error for nil amount")
	}
}

// TestBuildTree_DuplicateWallet: callers are expected to SUM by wallet at the
// source. If they hand us duplicates we fail.
func TestBuildTree_DuplicateWallet(t *testing.T) {
	a := addr(t, "0x5555555555555555555555555555555555555555")
	leaves := []Leaf{
		{Wallet: a, Amount: big.NewInt(1)},
		{Wallet: a, Amount: big.NewInt(2)},
	}
	_, _, err := BuildTree(leaves)
	if err == nil {
		t.Fatal("expected error for duplicate wallet")
	}
}

// TestBuildTree_SingleLeaf: the root IS the leaf hash, the proof is empty,
// and verification round-trips.
func TestBuildTree_SingleLeaf(t *testing.T) {
	a := addr(t, "0x6666666666666666666666666666666666666666")
	leaf := Leaf{Wallet: a, Amount: big.NewInt(1000)}

	root, proofs, err := BuildTree([]Leaf{leaf})
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	expected := expectedLeaf(t, a, leaf.Amount)
	if !bytes.Equal(root, expected) {
		t.Errorf("single-leaf root != leaf hash\n got:  %x\n want: %x", root, expected)
	}
	proof, ok := proofs[a]
	if !ok {
		t.Fatal("missing proof entry")
	}
	if len(proof) != 0 {
		t.Errorf("single-leaf proof should be empty, got %d siblings", len(proof))
	}
	if !VerifyProof(leaf, proof, root) {
		t.Error("VerifyProof failed for single leaf")
	}
}

// TestBuildTree_TwoLeaves: root = hashPair(leaf0, leaf1) sorted; each proof is
// the other leaf's hash.
func TestBuildTree_TwoLeaves(t *testing.T) {
	a1 := addr(t, "0x1111111111111111111111111111111111111111")
	a2 := addr(t, "0x2222222222222222222222222222222222222222")
	leaves := []Leaf{
		{Wallet: a1, Amount: big.NewInt(100)},
		{Wallet: a2, Amount: big.NewInt(200)},
	}

	root, proofs, err := BuildTree(leaves)
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}

	h1 := expectedLeaf(t, a1, leaves[0].Amount)
	h2 := expectedLeaf(t, a2, leaves[1].Amount)
	expectedRoot := hashPair(h1, h2)
	if !bytes.Equal(root, expectedRoot) {
		t.Errorf("two-leaf root mismatch\n got:  %x\n want: %x", root, expectedRoot)
	}

	if len(proofs[a1]) != 1 || !bytes.Equal(proofs[a1][0], h2) {
		t.Errorf("proof[a1] should be [h2], got %v", proofs[a1])
	}
	if len(proofs[a2]) != 1 || !bytes.Equal(proofs[a2][0], h1) {
		t.Errorf("proof[a2] should be [h1], got %v", proofs[a2])
	}

	for i, l := range leaves {
		if !VerifyProof(l, proofs[l.Wallet], root) {
			t.Errorf("VerifyProof failed for leaf %d", i)
		}
	}
}

// TestBuildTree_OutOfOrderInput: leaves are sorted by address bytes — the same
// inputs in any order MUST produce the same root.
func TestBuildTree_OutOfOrderInput(t *testing.T) {
	a1 := addr(t, "0x1111111111111111111111111111111111111111")
	a2 := addr(t, "0x2222222222222222222222222222222222222222")
	a3 := addr(t, "0x3333333333333333333333333333333333333333")

	asc := []Leaf{
		{Wallet: a1, Amount: big.NewInt(1)},
		{Wallet: a2, Amount: big.NewInt(2)},
		{Wallet: a3, Amount: big.NewInt(3)},
	}
	desc := []Leaf{asc[2], asc[1], asc[0]}

	r1, _, err := BuildTree(asc)
	if err != nil {
		t.Fatalf("BuildTree asc: %v", err)
	}
	r2, _, err := BuildTree(desc)
	if err != nil {
		t.Fatalf("BuildTree desc: %v", err)
	}
	if !bytes.Equal(r1, r2) {
		t.Errorf("root depends on input order: %x vs %x", r1, r2)
	}
}

// TestBuildTree_CallerSliceUnchanged: BuildTree must NOT mutate the caller's
// input slice (we sort an internal copy).
func TestBuildTree_CallerSliceUnchanged(t *testing.T) {
	a1 := addr(t, "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	a2 := addr(t, "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	in := []Leaf{
		{Wallet: a2, Amount: big.NewInt(2)},
		{Wallet: a1, Amount: big.NewInt(1)},
	}
	original := []Leaf{in[0], in[1]}

	if _, _, err := BuildTree(in); err != nil {
		t.Fatalf("BuildTree: %v", err)
	}
	for i := range in {
		if in[i] != original[i] {
			t.Errorf("BuildTree mutated caller input at %d", i)
		}
	}
}

// TestBuildTree_VerifyAll exercises 3, 5, and 7 leaves. For each tree we
// verify every leaf proves AND that perturbing the amount fails the proof.
func TestBuildTree_VerifyAll(t *testing.T) {
	for _, n := range []int{3, 5, 7, 10, 16} {
		t.Run("n="+itoa(n), func(t *testing.T) {
			leaves := make([]Leaf, n)
			for i := 0; i < n; i++ {
				// Spread addresses across the byte space so sorting matters.
				var a common.Address
				a[0] = byte(i * 37)
				a[19] = byte(i + 1)
				leaves[i] = Leaf{Wallet: a, Amount: big.NewInt(int64(i+1) * 1000)}
			}
			root, proofs, err := BuildTree(leaves)
			if err != nil {
				t.Fatalf("BuildTree: %v", err)
			}
			for i, l := range leaves {
				if !VerifyProof(l, proofs[l.Wallet], root) {
					t.Errorf("leaf %d (%s) failed verification", i, l.Wallet.Hex())
				}
				// Perturb amount — the proof should now fail.
				perturbed := Leaf{Wallet: l.Wallet, Amount: new(big.Int).Add(l.Amount, big.NewInt(1))}
				if VerifyProof(perturbed, proofs[l.Wallet], root) {
					t.Errorf("leaf %d perturbed amount still verified — proof is too permissive", i)
				}
			}

			// Wrong proof entirely (substitute another leaf's proof) should fail.
			if n >= 2 {
				if VerifyProof(leaves[0], proofs[leaves[1].Wallet], root) {
					t.Error("leaf[0] verified against leaf[1]'s proof — broken proof")
				}
			}
		})
	}
}

// TestVerifyProof_RootLengthGuard: a root with non-32 length must reject
// without panicking. Defends against API callers handing in mangled input.
func TestVerifyProof_RootLengthGuard(t *testing.T) {
	a := addr(t, "0x7777777777777777777777777777777777777777")
	leaf := Leaf{Wallet: a, Amount: big.NewInt(1)}
	if VerifyProof(leaf, nil, []byte{0x00, 0x01}) {
		t.Error("VerifyProof accepted a 2-byte root")
	}
}

// TestVerifyProof_SiblingLengthGuard: same guard for proof entries.
func TestVerifyProof_SiblingLengthGuard(t *testing.T) {
	a := addr(t, "0x8888888888888888888888888888888888888888")
	leaf := Leaf{Wallet: a, Amount: big.NewInt(1)}
	root := EncodeLeaf(leaf)
	bad := [][]byte{{0x00}}
	if VerifyProof(leaf, bad, root) {
		t.Error("VerifyProof accepted a 1-byte sibling")
	}
}

// TestHexProofRoundTrip: HexProof and DecodeHexProof must be inverses.
func TestHexProofRoundTrip(t *testing.T) {
	in := [][]byte{
		bytes.Repeat([]byte{0xab}, 32),
		bytes.Repeat([]byte{0xcd}, 32),
	}
	hexed := HexProof(in)
	for i, h := range hexed {
		if len(h) != 2+64 {
			t.Errorf("HexProof[%d] length=%d, want 66", i, len(h))
		}
	}
	out, err := DecodeHexProof(hexed)
	if err != nil {
		t.Fatalf("DecodeHexProof: %v", err)
	}
	if len(out) != len(in) {
		t.Fatalf("round-trip length mismatch: %d vs %d", len(out), len(in))
	}
	for i := range in {
		if !bytes.Equal(in[i], out[i]) {
			t.Errorf("element %d differs: %x vs %x", i, in[i], out[i])
		}
	}
}

// TestDecodeHexProof_RejectsWrongLength catches operators submitting 64-bit
// or otherwise-malformed bytes.
func TestDecodeHexProof_RejectsWrongLength(t *testing.T) {
	if _, err := DecodeHexProof([]string{"0x" + hex.EncodeToString(bytes.Repeat([]byte{0xff}, 16))}); err == nil {
		t.Error("expected error for 16-byte sibling")
	}
}

// itoa avoids strconv just to keep the test file's imports minimal. n is
// small (single-digit + double-digit), so this loop is fine.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

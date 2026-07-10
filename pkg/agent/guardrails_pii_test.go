package agent

import (
	"context"
	"strings"
	"testing"
)

// A Luhn-valid 16-digit test card (Visa test number) and a Luhn-valid bank card.
const (
	testVisa     = "4111111111111111" // valid Luhn, 16 digits
	testVisaSpc  = "4111 1111 1111 1111"
	testCNIDGood = "110101199003074610" // checksum-valid sample id
	testCNIDBad  = "110101199003074619" // wrong checkdigit
)

func runPII(content string, kinds []PIIKind, mode RedactMode) (string, []PIIKind, bool) {
	if len(kinds) == 0 {
		kinds = AllPIIKinds
	}
	return redactPII(content, detectorsFor(kinds), mode)
}

func TestPIIDetectEachKindMask(t *testing.T) {
	// A Luhn-valid 16-digit bank number distinct from the credit-card grouped form.
	bank := luhnComplete("453201511283003") // 15 known digits + computed check
	cases := []struct {
		name    string
		kind    PIIKind
		input   string
		mustHit bool
	}{
		{"email", PIIEmail, "reach me at alice@example.com please", true},
		{"phone_us", PIIPhoneUS, "call 555-123-4567 today", true},
		{"credit_card", PIICreditCard, "card " + testVisaSpc + " exp", true},
		{"ssn", PIISSN, "ssn 123-45-6789 on file", true},
		{"cn_id", PIICNID, "身份证 " + testCNIDGood + " 已登记", true},
		{"cn_mobile", PIICNMobile, "手机 13812345678 联系", true},
		{"bank_card", PIIBankCard, "账号 " + bank + " 转账", true},
		{"ipv4", PIIIPv4, "server 192.168.1.100 down", true},
		{"passport", PIIPassport, "passport E12345678 issued", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, detected, blocked := runPII(tc.input, []PIIKind{tc.kind}, RedactMask)
			if blocked {
				t.Fatalf("mask mode must never block")
			}
			hit := false
			for _, k := range detected {
				if k == tc.kind {
					hit = true
				}
			}
			if hit != tc.mustHit {
				t.Fatalf("kind %s detected=%v want %v (out=%q)", tc.kind, hit, tc.mustHit, out)
			}
			if tc.mustHit {
				token := "[REDACTED_" + strings.ToUpper(string(tc.kind)) + "]"
				if !strings.Contains(out, token) {
					t.Fatalf("expected %s in output, got %q", token, out)
				}
			}
		})
	}
}

func TestPIIPartialFormats(t *testing.T) {
	cases := []struct {
		name  string
		kind  PIIKind
		input string
		want  string
	}{
		{"email", PIIEmail, "alice@example.com", "a***@example.com"},
		{"cn_mobile", PIICNMobile, "13812345678", "138****5678"},
		{"phone_us", PIIPhoneUS, "555-123-4567", "555****4567"},
		{"cn_id", PIICNID, testCNIDGood, "110***********4610"},
		{"ssn", PIISSN, "123-45-6789", "***-**-6789"},
		{"ipv4", PIIIPv4, "192.168.1.100", "192.*.*.*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, detected, _ := runPII(tc.input, []PIIKind{tc.kind}, RedactPartial)
			if len(detected) == 0 {
				t.Fatalf("nothing detected in %q", tc.input)
			}
			if out != tc.want {
				t.Fatalf("partial redact = %q want %q", out, tc.want)
			}
		})
	}
}

func TestPIICreditCardPartialKeepsLast4(t *testing.T) {
	out, _, _ := runPII(testVisa, []PIIKind{PIICreditCard, PIIBankCard}, RedactPartial)
	if !strings.HasSuffix(out, "1111") || strings.Contains(out, "4111111111111") {
		t.Fatalf("card partial should keep only last 4: %q", out)
	}
}

func TestPIIHashStability(t *testing.T) {
	in := "alice@example.com"
	a, _, _ := runPII(in, []PIIKind{PIIEmail}, RedactHash)
	b, _, _ := runPII(in, []PIIKind{PIIEmail}, RedactHash)
	if a != b {
		t.Fatalf("hash redaction not stable: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "[EMAIL:") || !strings.HasSuffix(a, "]") {
		t.Fatalf("unexpected hash token format: %q", a)
	}
	// A different value must map to a different token.
	c, _, _ := runPII("bob@example.com", []PIIKind{PIIEmail}, RedactHash)
	if c == a {
		t.Fatalf("distinct inputs collided: %q", a)
	}
}

func TestPIIBlockMode(t *testing.T) {
	out, detected, blocked := runPII("email alice@example.com here", []PIIKind{PIIEmail}, RedactBlock)
	if !blocked {
		t.Fatalf("expected block")
	}
	if len(detected) == 0 {
		t.Fatalf("expected detected kinds on block")
	}
	if out != "email alice@example.com here" {
		t.Fatalf("block mode must not rewrite content, got %q", out)
	}
}

func TestPIICNIDChecksum(t *testing.T) {
	if !validCNID(testCNIDGood) {
		t.Fatalf("valid CN id rejected: %s", testCNIDGood)
	}
	if validCNID(testCNIDBad) {
		t.Fatalf("invalid CN id accepted: %s", testCNIDBad)
	}
	// Detection must reject the bad checksum (no false positive).
	_, detected, _ := runPII("id "+testCNIDBad+" x", []PIIKind{PIICNID}, RedactMask)
	for _, k := range detected {
		if k == PIICNID {
			t.Fatalf("bad-checksum CN id should not be detected as cn_id")
		}
	}
}

func TestPIILuhn(t *testing.T) {
	if !validLuhn(testVisa) {
		t.Fatalf("valid Luhn number rejected")
	}
	if validLuhn("4111111111111112") {
		t.Fatalf("invalid Luhn number accepted")
	}
	// A 16-digit non-Luhn run must not be redacted as a bank card.
	_, detected, _ := runPII("num 1234567812345670x", []PIIKind{PIIBankCard}, RedactMask)
	_ = detected
	bad := "1234567812345671" // not Luhn-valid
	_, det2, _ := runPII("num "+bad+" end", []PIIKind{PIIBankCard}, RedactMask)
	for _, k := range det2 {
		if k == PIIBankCard {
			t.Fatalf("non-Luhn 16-digit run wrongly flagged as bank_card")
		}
	}
}

func TestPIIOverlapResolutionPrefersCNID(t *testing.T) {
	// An 18-digit checksum-valid CN id also falls in the bank-card 13-19 range.
	// The higher-priority, checksum-validated CN id must win.
	out, detected, _ := runPII("身份证 "+testCNIDGood, []PIIKind{PIICNID, PIIBankCard}, RedactMask)
	if len(detected) != 1 || detected[0] != PIICNID {
		t.Fatalf("expected only cn_id, got %v (out=%q)", detected, out)
	}
	if !strings.Contains(out, "[REDACTED_CN_ID]") {
		t.Fatalf("expected cn_id redaction, got %q", out)
	}
}

func TestPIIMultipleKindsInOnePass(t *testing.T) {
	in := "mail alice@example.com phone 13812345678 ip 10.0.0.1"
	out, detected, _ := runPII(in, nil, RedactMask)
	if len(detected) < 3 {
		t.Fatalf("expected >=3 kinds, got %v", detected)
	}
	if strings.Contains(out, "alice@example.com") || strings.Contains(out, "13812345678") {
		t.Fatalf("output still contains raw PII: %q", out)
	}
}

func TestPIIGuardrailWrappsResult(t *testing.T) {
	g := NewPIIGuardrail(PIIConfig{Mode: RedactPartial, Kind: GuardrailKindInput})
	res, err := g.Check(context.Background(), "reach alice@example.com", GuardrailKindInput)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !res.Modified || res.Content == "" {
		t.Fatalf("expected modified result, got %+v", res)
	}
	if _, ok := res.Metadata["pii_kinds"]; !ok {
		t.Fatalf("expected pii_kinds metadata, got %+v", res.Metadata)
	}
	// A guardrail scoped to input must be a no-op when asked for output.
	res2, err := g.Check(context.Background(), "reach alice@example.com", GuardrailKindOutput)
	if err != nil {
		t.Fatalf("check output: %v", err)
	}
	if res2.Modified {
		t.Fatalf("input-scoped guardrail should not modify on output kind")
	}
}

func TestPIICleanContentPasses(t *testing.T) {
	out, detected, blocked := runPII("just a normal sentence with no secrets", nil, RedactMask)
	if blocked || len(detected) != 0 {
		t.Fatalf("clean content should not trip guardrail: detected=%v blocked=%v", detected, blocked)
	}
	if out != "just a normal sentence with no secrets" {
		t.Fatalf("clean content mutated: %q", out)
	}
}

// luhnComplete appends the correct Luhn check digit to a digit string.
func luhnComplete(prefix string) string {
	// Compute check digit so that the full number passes Luhn.
	sum := 0
	alt := true // position of the (future) check digit is even from the right => the last prefix digit is doubled
	for i := len(prefix) - 1; i >= 0; i-- {
		d := int(prefix[i] - '0')
		if alt {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	check := (10 - (sum % 10)) % 10
	return prefix + string(rune('0'+check))
}

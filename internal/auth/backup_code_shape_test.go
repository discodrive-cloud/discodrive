package auth

import "testing"

// A mistyped TOTP code must not reach the backup-code check: that check runs Argon2
// against every unused backup code. Only input shaped like a backup code goes there.
func TestOnlyBackupShapedInputIsCheckedAgainstBackupCodes(t *testing.T) {
	plain, _, err := newBackupCode()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		in   string
		want bool
	}{
		{plain, true},
		{" " + plain[:4] + plain[5:] + " ", true}, // no dash, stray spaces
		{"ABCD-2345", true},
		{"123456", false},     // a TOTP code
		{"123 456", false},    // a TOTP code as authenticators print it
		{"12345678", false},   // 8 chars, but 1, 8 and 0 are not in the alphabet
		{"abcd-234", false},   // too short
		{"abcd-23456", false}, // too long
		{"", false},
	} {
		if got := backupCodeShaped(normalizeBackupCode(c.in)); got != c.want {
			t.Errorf("backupCodeShaped(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

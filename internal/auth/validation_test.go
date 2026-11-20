package auth

import (
	"strings"
	"testing"
)

func TestValidation(t *testing.T) {
	for _, s := range []string{"Abc123!x", "한글Ab12!x"} {
		if !ValidPassword(s) {
			t.Errorf("valid rejected")
		}
	}
	for _, s := range []string{"short1!", "abcdefgh", "12345678!", "Abcdef12", "Abc 123!", strings.Repeat("A", 21) + "1!"} {
		if ValidPassword(s) {
			t.Error("invalid accepted")
		}
	}
	if s, e := NormalizeEmail(" Demo@EXAMPLE.com "); e != nil || s != "demo@example.com" {
		t.Fatal(s, e)
	}
	for _, s := range []string{"a", "Name <a@example.com>", "a@", "a@localhost"} {
		if _, e := NormalizeEmail(s); e == nil {
			t.Error("invalid email accepted")
		}
	}
	if _, key, e := Nickname("테스터Ab"); e != nil || key != "테스터ab" {
		t.Fatal(key, e)
	}
	for _, s := range []string{"x", "two words", "공백　", "zero\u200bwidth"} {
		if _, _, e := Nickname(s); e == nil {
			t.Error("invalid nickname")
		}
	}
}

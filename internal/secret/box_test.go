package secret

import "testing"

func TestRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := box.Encrypt("ssh-secret")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "ssh-secret" {
		t.Fatal("stored value is plaintext")
	}
	got, err := box.Decrypt(enc)
	if err != nil || got != "ssh-secret" {
		t.Fatalf("got %q err %v", got, err)
	}
}

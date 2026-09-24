package creds_test

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joeyshi12/icebreaker/internal/creds"
)

func TestMintedPasswordIsTheHMACCoturnWouldCompute(t *testing.T) {
	c := creds.New("sekrit", time.Hour)
	username, password := c.Mint()

	// this is the HMAC coturn computes for the same username under use-auth-secret
	mac := hmac.New(sha1.New, []byte("sekrit"))
	mac.Write([]byte(username))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if password != want {
		t.Fatalf("password %q, want %q", password, want)
	}
}

// Nothing here validates a credential any more, so the expiry written into the
// username is all that stands between a leaked password and an unbounded one.
func TestExpiryIsNowPlusTheTTL(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	for _, ttl := range []time.Duration{time.Minute, time.Hour, 24 * time.Hour} {
		c := creds.New("sekrit", ttl)
		c.Now = func() time.Time { return base }
		username, _ := c.Mint()
		stamp, _, ok := strings.Cut(username, ":")
		if !ok {
			t.Fatalf("username %q has no timestamp half", username)
		}
		expiry, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil {
			t.Fatalf("timestamp %q is not a unix time: %v", stamp, err)
		}
		if want := base.Add(ttl).Unix(); expiry != want {
			t.Fatalf("expiry %d with a %v ttl, want %d", expiry, ttl, want)
		}
	}
}

func TestMintedUsernameNamesNoApp(t *testing.T) {
	c := creds.New("sekrit", time.Hour)
	c.Now = func() time.Time { return time.Unix(1_000_000, 0) }
	username, _ := c.Mint()
	if want := "1003600:peer"; username != want {
		t.Fatalf("username %q, want %q: the name half goes out to every client", username, want)
	}
}

func TestNoSecretMeansNoCredentials(t *testing.T) {
	if creds.New("", time.Hour).Enabled() {
		t.Fatal("credentials without a secret should not be enabled")
	}
}

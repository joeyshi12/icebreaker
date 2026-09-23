// Package creds mints ephemeral TURN credentials for a relay running elsewhere.
package creds

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"time"
)

type Credentials struct {
	Secret string
	TTL    time.Duration
	Name   string

	// Now is the clock, so tests can move time. Nil means time.Now.
	Now func() time.Time
}

func New(secret string, ttl time.Duration) Credentials {
	return Credentials{Secret: secret, TTL: ttl, Name: "peer"}
}

func (c Credentials) Enabled() bool { return c.Secret != "" }

// Mint returns a username and password valid until now plus the TTL. Nothing here
// checks them: the relay holds the same secret and recomputes the HMAC, which is
// why no credential is ever provisioned or stored per player.
func (c Credentials) Mint() (username, password string) {
	username = fmt.Sprintf("%d:%s", c.clock().Add(c.TTL).Unix(), c.name())
	return username, c.sign(username)
}

// coturn's use-auth-secret scheme: base64 HMAC-SHA1 of "timestamp:name" under the shared secret.
func (c Credentials) sign(username string) string {
	mac := hmac.New(sha1.New, []byte(c.Secret))
	mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (c Credentials) clock() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Credentials) name() string {
	if c.Name == "" {
		return "peer"
	}
	return c.Name
}

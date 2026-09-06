package rasvn

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/otuschhoff/go-svn/auth"
	"github.com/otuschhoff/go-svn/svn"
)

func (conn *connection) runSASL(ctx context.Context, mechanisms []string, realm string) error {
	available := make(map[string]bool, len(mechanisms))
	for _, mechanism := range mechanisms {
		available[mechanism] = true
	}
	if available["ANONYMOUS"] {
		hostname, _ := os.Hostname()
		return conn.exchangeSASL("ANONYMOUS", encodeSASL("anonymous@"+hostname), nil)
	}
	if available["EXTERNAL"] && conn.secure {
		return conn.exchangeSASL("EXTERNAL", encodeSASL(conn.username), nil)
	}
	credentials, err := conn.credentials(ctx, realm)
	if err != nil {
		return err
	}
	rejected := make([]*auth.Credentials, 0)
	for {
		var exchangeErr error
		if available["CRAM-MD5"] {
			challenge := func(token string) (string, error) {
				mac := hmac.New(md5.New, []byte(credentials.Password))
				_, _ = mac.Write([]byte(token))
				return credentials.Username + " " + hex.EncodeToString(mac.Sum(nil)), nil
			}
			exchangeErr = conn.exchangeSASL("CRAM-MD5", "", challenge)
		} else if available["PLAIN"] && conn.secure {
			token := "\x00" + credentials.Username + "\x00" + credentials.Password
			exchangeErr = conn.exchangeSASL("PLAIN", encodeSASL(token), nil)
		} else {
			return fmt.Errorf("%w: server mechanisms %s", svn.ErrAuthnFailed, strings.Join(mechanisms, ", "))
		}
		if exchangeErr == nil || !errors.Is(exchangeErr, svn.ErrAuthnFailed) {
			return exchangeErr
		}
		rejected = append(rejected, credentials)
		credentials, err = conn.callbacks.Auth.GetCredentialsAfter(ctx, auth.Simple, realm, credentials.Username, rejected...)
		if err != nil {
			return err
		}
		conn.username, conn.password = credentials.Username, credentials.Password
	}
}

func (conn *connection) credentials(ctx context.Context, realm string) (*auth.Credentials, error) {
	if conn.username != "" && conn.password != "" {
		return &auth.Credentials{Kind: auth.Simple, Realm: realm, Username: conn.username, Password: conn.password}, nil
	}
	credentials, err := conn.callbacks.Auth.GetCredentials(ctx, auth.Simple, realm, conn.username)
	if err != nil {
		return nil, err
	}
	conn.username, conn.password = credentials.Username, credentials.Password
	return credentials, nil
}

func (conn *connection) exchangeSASL(mechanism, initial string, challenge func(string) (string, error)) error {
	initialItems := []Item{}
	if initial != "" {
		initialItems = append(initialItems, String([]byte(initial)))
	}
	if err := conn.write(List(Word(mechanism), List(initialItems...))); err != nil {
		return err
	}
	for {
		item, err := conn.reader.Decode()
		if err != nil {
			return err
		}
		if item.Kind != ListKind || len(item.List) != 2 || item.List[0].Kind != WordKind || item.List[1].Kind != ListKind {
			return malformed("invalid SASL challenge")
		}
		switch item.List[0].Word {
		case "success":
			return nil
		case "failure":
			message := "authentication failed"
			if len(item.List[1].List) > 0 && item.List[1].List[0].Kind == StringKind {
				message = string(item.List[1].List[0].String)
			}
			return fmt.Errorf("%w: %s", svn.ErrAuthnFailed, message)
		case "step":
			if challenge == nil || len(item.List[1].List) != 1 || item.List[1].List[0].Kind != StringKind {
				return malformed("unexpected SASL step")
			}
			response, err := challenge(string(item.List[1].List[0].String))
			if err != nil {
				return err
			}
			if err := conn.write(String([]byte(response))); err != nil {
				return err
			}
		default:
			return malformed("unknown SASL status %q", item.List[0].Word)
		}
	}
}

func encodeSASL(value string) string { return base64.StdEncoding.EncodeToString([]byte(value)) }

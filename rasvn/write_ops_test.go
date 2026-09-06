package rasvn

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"

	"github.com/oliver-tuschhoff/go-svn/svn"
)

func TestChangeRevProp2Wire(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	session := &Session{conn: newConnection(client, nil, "", "", false), capabilities: map[string]bool{"atomic-revprops": true}}
	done := make(chan error, 1)
	go func() {
		reader, writer := NewReader(server), NewWriter(server)
		item, err := reader.Decode()
		if err != nil {
			done <- err
			return
		}
		want := List(Word("change-rev-prop2"), List(Number(3), String([]byte("custom:p")), List(String([]byte("new"))), List(Word("false"), String([]byte("old")))))
		if !reflect.DeepEqual(item, want) {
			done <- malformed("command=%#v, want %#v", item, want)
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		done <- writeTestItem(writer, response("success"))
	}()
	if err := session.ChangeRevProp(context.Background(), 3, "custom:p", []byte("new"), []byte("old"), false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChangeRevProp2ExpectedAbsentWire(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	session := &Session{conn: newConnection(client, nil, "", "", false), capabilities: map[string]bool{"atomic-revprops": true}}
	done := make(chan error, 1)
	go func() {
		reader, writer := NewReader(server), NewWriter(server)
		item, err := reader.Decode()
		if err != nil {
			done <- err
			return
		}
		want := List(Word("change-rev-prop2"), List(Number(3), String([]byte("custom:p")), List(String([]byte("new"))), List(Word("false"))))
		if !reflect.DeepEqual(item, want) {
			done <- malformed("command=%#v, want %#v", item, want)
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		done <- writeTestItem(writer, response("success"))
	}()
	if err := session.ChangeRevProp(context.Background(), 3, "custom:p", []byte("new"), nil, false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLockManyMixedResults(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	session := &Session{conn: newConnection(client, nil, "", "", false), capabilities: make(map[string]bool)}
	done := make(chan error, 1)
	go func() {
		reader, writer := NewReader(server), NewWriter(server)
		command, err := reader.Decode()
		if err != nil || command.List[0].Word != "lock-many" {
			done <- errors.Join(err, malformed("expected lock-many"))
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		lock := List(String([]byte("a")), String([]byte("token")), String([]byte("owner")), List(), String([]byte("2020-01-01T00:00:00.000000Z")), List())
		if err := writeTestItem(writer, List(Word("success"), lock)); err != nil {
			done <- err
			return
		}
		failure := List(Number(uint64(svn.ErrFSPathAlreadyLocked)), String([]byte("busy")), String(nil), Number(0))
		if err := writeTestItem(writer, List(Word("failure"), List(failure))); err != nil {
			done <- err
			return
		}
		if err := writeTestItem(writer, Word("done")); err != nil {
			done <- err
			return
		}
		done <- writeTestItem(writer, response("success"))
	}()
	var paths []string
	var callbackErrors []error
	err := session.Lock(context.Background(), map[string]svn.Revnum{"b": 1, "a": 1}, "note", false, func(path string, lock *svn.Lock, err error) error {
		paths = append(paths, path)
		callbackErrors = append(callbackErrors, err)
		if path == "a" && (lock == nil || lock.Token != "token") {
			t.Fatalf("lock=%#v", lock)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"a", "b"}) || callbackErrors[0] != nil || !errors.Is(callbackErrors[1], svn.ErrFSPathAlreadyLocked) {
		t.Fatalf("paths=%v errors=%v", paths, callbackErrors)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLockFallsBackToSingleCommand(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	session := &Session{conn: newConnection(client, nil, "", "", false), capabilities: make(map[string]bool)}
	done := make(chan error, 1)
	go func() {
		reader, writer := NewReader(server), NewWriter(server)
		first, err := reader.Decode()
		if err != nil || first.List[0].Word != "lock-many" {
			done <- errors.Join(err, malformed("expected lock-many"))
			return
		}
		unknown := List(Number(uint64(svn.ErrRASvnUnknownCmd)), String([]byte("unknown")), String(nil), Number(0))
		if err := writeTestItem(writer, List(Word("failure"), List(unknown))); err != nil {
			done <- err
			return
		}
		fallback, err := reader.Decode()
		if err != nil || fallback.List[0].Word != "lock" {
			done <- errors.Join(err, malformed("expected lock fallback"))
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		lock := List(String([]byte("a")), String([]byte("token")), String([]byte("owner")), List(), String([]byte("2020-01-01T00:00:00.000000Z")), List())
		done <- writeTestItem(writer, response("success", lock))
	}()
	var got *svn.Lock
	if err := session.Lock(context.Background(), map[string]svn.Revnum{"a": 1}, "", false, func(_ string, lock *svn.Lock, err error) error {
		got = lock
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Token != "token" {
		t.Fatalf("lock=%#v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUnlockManyAndCallbackErrorDrain(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	session := &Session{conn: newConnection(client, nil, "", "", false), capabilities: make(map[string]bool)}
	done := make(chan error, 1)
	go func() {
		reader, writer := NewReader(server), NewWriter(server)
		command, err := reader.Decode()
		if err != nil || command.List[0].Word != "unlock-many" {
			done <- errors.Join(err, malformed("expected unlock-many"))
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		for _, name := range []string{"a", "b"} {
			if err := writeTestItem(writer, List(Word("success"), List(String([]byte(name))))); err != nil {
				done <- err
				return
			}
		}
		if err := writeTestItem(writer, Word("done")); err != nil {
			done <- err
			return
		}
		if err := writeTestItem(writer, response("success")); err != nil {
			done <- err
			return
		}
		followup, err := reader.Decode()
		if err != nil || followup.List[0].Word != "get-latest-rev" {
			done <- errors.Join(err, malformed("expected follow-up command"))
			return
		}
		if err := writeTestItem(writer, response("success", List(), String(nil))); err != nil {
			done <- err
			return
		}
		done <- writeTestItem(writer, response("success", Number(9)))
	}()
	callbackErr := errors.New("stop callbacks")
	calls := 0
	err := session.Unlock(context.Background(), map[string]string{"b": "tb", "a": "ta"}, false, func(string, *svn.Lock, error) error {
		calls++
		return callbackErr
	})
	if !errors.Is(err, callbackErr) || calls != 1 {
		t.Fatalf("unlock error=%v calls=%d", err, calls)
	}
	if revision, err := session.LatestRevision(context.Background()); err != nil || revision != 9 {
		t.Fatalf("follow-up revision=%d error=%v", revision, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

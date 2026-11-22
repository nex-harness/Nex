package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestRelayBlindPipeLargeAndConcurrentTunnels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := New(Config{KeepAliveInterval: 500 * time.Millisecond})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	nodeID := "nex_testnode_001"
	hostCtx, hostCancel := context.WithCancel(ctx)
	defer hostCancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- DialHostControl(hostCtx, ts.URL, nodeID, "", func(_ context.Context, _ string, conn net.Conn) {
			defer conn.Close()
			// Simple blind echo server over the relayed net.Conn
			_, _ = io.Copy(conn, conn)
		})
	}()

	// Wait until host control registers
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.Stats().OnlineHosts == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if srv.Stats().OnlineHosts != 1 {
		t.Fatalf("expected 1 online host, got %+v", srv.Stats())
	}

	// Open two concurrent tunnels and transfer >256KB binary payloads cleanly
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cConn, err := DialClientTunnel(ctx, ts.URL, nodeID, "")
			if err != nil {
				t.Errorf("tunnel %d DialClientTunnel: %v", idx, err)
				return
			}
			defer cConn.Close()

			payload := bytes.Repeat([]byte{byte('A' + idx), 0x00, 0xFF, 0x7A}, 70*1024) // 280KB
			go func() {
				_, _ = cConn.Write(payload)
			}()
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(cConn, got); err != nil {
				t.Errorf("tunnel %d ReadFull: %v", idx, err)
				return
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("tunnel %d echo mismatch (len=%d)", idx, len(got))
			}
		}(i)
	}
	wg.Wait()

	// Wait briefly for relay tunnel counters to drain cleanly
	drainDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(drainDeadline) {
		if srv.Stats().ActiveTunnels == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := srv.Stats()
	if st.ActiveTunnels != 0 {
		t.Fatalf("expected active tunnels to drain to 0, got %+v", st)
	}

	hostCancel()
	<-errCh
}

func TestRelayAuthOfflineAndReconnectSafety(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := New(Config{AuthToken: "secret-relay-key", AcceptTimeout: 500 * time.Millisecond})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Health endpoint exposes status without requiring auth token
	resp, err := http.Get(ts.URL + "/relay/v1/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("health check failed: resp=%v err=%v", resp, err)
	}
	var health map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&health)
	_ = resp.Body.Close()
	if health["auth_required"] != true || health["ok"] != true {
		t.Fatalf("unexpected health payload: %+v", health)
	}

	// 2. Client dial without token fails (401)
	if _, err := DialClientTunnel(ctx, ts.URL, "nex_missing_1", ""); err == nil {
		t.Fatalf("expected unauthorized error when AuthToken missing")
	}

	// 3. Authorized dial to offline host fails cleanly
	if _, err := DialClientTunnel(ctx, ts.URL, "nex_offline_1", "secret-relay-key"); err == nil {
		t.Fatalf("expected error for offline host")
	}

	// 4. Reconnecting the same node_id cleanly replaces previous host without dropping the new one
	host1Ctx, host1Cancel := context.WithCancel(ctx)
	host2Ctx, host2Cancel := context.WithCancel(ctx)
	defer host2Cancel()

	nodeID := "nex_reconn_1"
	h1Done := make(chan struct{})
	go func() {
		_ = DialHostControl(host1Ctx, ts.URL, nodeID, "secret-relay-key", func(_ context.Context, _ string, conn net.Conn) {
			_ = conn.Close()
		})
		close(h1Done)
	}()

	for time.Now().Add(2*time.Second).After(time.Now()) && srv.Stats().OnlineHosts == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	go func() {
		_ = DialHostControl(host2Ctx, ts.URL, nodeID, "secret-relay-key", func(_ context.Context, _ string, conn net.Conn) {
			defer conn.Close()
			_, _ = conn.Write([]byte("HOST2_OK"))
		})
	}()

	// Wait until host1 exits because host2 replaced it
	select {
	case <-h1Done:
	case <-time.After(3 * time.Second):
		t.Fatal("expected replaced host1 control loop to exit")
	}
	host1Cancel()

	if srv.Stats().OnlineHosts != 1 {
		t.Fatalf("expected host2 to stay registered after host1 exited, stats=%+v", srv.Stats())
	}

	cConn, err := DialClientTunnel(ctx, ts.URL, nodeID, "secret-relay-key")
	if err != nil {
		t.Fatalf("dial host2 after reconnect failed: %v", err)
	}
	defer cConn.Close()
	buf := make([]byte, len("HOST2_OK"))
	if _, err := io.ReadFull(cConn, buf); err != nil || string(buf) != "HOST2_OK" {
		t.Fatalf("expected HOST2_OK from second host, got %q err=%v", string(buf), err)
	}
}

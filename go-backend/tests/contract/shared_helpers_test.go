package contract_test

// 本文件收纳原先寄居在联邦契约测试文件中的通用助手（mock 节点会话、
// 状态等待、回执解包），它们被多个非联邦契约测试共用；联邦契约文件
// 随 DEV-RM-SHARE 删除后，助手在此保留、实现逐字未动。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"go-backend/internal/http/response"
	"go-backend/internal/security"
	"go-backend/internal/store/repo"
)

func startMockNodeSession(t *testing.T, baseURL string, nodeSecret string) func() {
	return startMockNodeSessionWithHook(t, baseURL, nodeSecret, nil)
}

func startMockNodeSessionWithHook(t *testing.T, baseURL string, nodeSecret string, onCommand func(cmdType string)) func() {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse provider url: %v", err)
	}
	if strings.EqualFold(u.Scheme, "https") {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/system-info"
	q := u.Query()
	q.Set("type", "1")
	q.Set("secret", nodeSecret)
	q.Set("version", "v1")
	q.Set("http", "1")
	q.Set("tls", "1")
	q.Set("socks", "1")
	u.RawQuery = q.Encode()

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial mock node websocket: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, raw, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}

			plain := raw
			var wrap struct {
				Encrypted bool   `json:"encrypted"`
				Data      string `json:"data"`
			}
			if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Encrypted && strings.TrimSpace(wrap.Data) != "" {
				crypto, cryptoErr := security.NewAESCrypto(nodeSecret)
				if cryptoErr == nil {
					if dec, decErr := crypto.Decrypt(wrap.Data); decErr == nil {
						plain = []byte(dec)
					}
				}
			}

			var cmd struct {
				Type      string `json:"type"`
				RequestID string `json:"requestId"`
			}
			if err := json.Unmarshal(plain, &cmd); err != nil {
				continue
			}
			if strings.TrimSpace(cmd.RequestID) == "" {
				continue
			}
			if onCommand != nil {
				onCommand(strings.TrimSpace(cmd.Type))
			}

			respType := fmt.Sprintf("%sResponse", cmd.Type)
			respPayload := map[string]interface{}{
				"type":      respType,
				"success":   true,
				"message":   "OK",
				"requestId": cmd.RequestID,
			}
			if strings.EqualFold(strings.TrimSpace(cmd.Type), "TcpPing") {
				respPayload["data"] = map[string]interface{}{
					"success":     true,
					"averageTime": 8.5,
					"packetLoss":  0,
					"message":     "mock tcp ok",
				}
			}
			respBytes, err := json.Marshal(respPayload)
			if err != nil {
				continue
			}
			_ = conn.WriteMessage(websocket.TextMessage, respBytes)
		}
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			_ = conn.Close()
			wg.Wait()
		})
	}
}

func waitNodeStatus(t *testing.T, r *repo.Repository, nodeID int64, expectedStatus int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, err := tryQueryInt(t, r, `SELECT status FROM node WHERE id = ?`, nodeID)
		if err == nil && status == expectedStatus {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("node %d status did not reach %d before timeout", nodeID, expectedStatus)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func requestContractEnvelope(t *testing.T, router http.Handler, token string, path string, body interface{}) response.R {
	t.Helper()

	payload := []byte("{}")
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body for %s: %v", path, err)
		}
		payload = raw
	}

	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("expected http 200 for %s, got %d", path, res.Code)
	}

	var out response.R
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode response for %s: %v", path, err)
	}

	return out
}

func mustContractSlice(t *testing.T, data interface{}, label string) []interface{} {
	t.Helper()

	rows, ok := data.([]interface{})
	if !ok {
		t.Fatalf("expected %s to be []interface{}, got %T", label, data)
	}
	return rows
}

func contractValueAsInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case json.Number:
		i, err := n.Int64()
		if err == nil {
			return i
		}
		f, err := n.Float64()
		if err == nil {
			return int64(f)
		}
		return 0
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		if err == nil {
			return i
		}
		return 0
	default:
		return 0
	}
}

func contractValueAsString(v interface{}) string {
	s, _ := v.(string)
	return s
}

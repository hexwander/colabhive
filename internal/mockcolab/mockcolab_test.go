package mockcolab_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/colabhive/colabhive/internal/events"
	"github.com/colabhive/colabhive/internal/mockcolab"
	"github.com/colabhive/colabhive/internal/models"
	"github.com/colabhive/colabhive/internal/pool"
	"github.com/colabhive/colabhive/internal/store"
	"github.com/gorilla/websocket"
)

func TestMockColabEndToEndTunnelJupyterAndZMQWebSocket(t *testing.T) {
	srv := mockcolab.NewServer()
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Verify HTTP 400 GPU entitlement block (Pro #3 lacks H100)
	nbhBad := models.NewNotebookHash()
	getAssignReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/tun/m/assign?nbh="+nbhBad+"&variant=GPU&accelerator=H100&authuser=0", nil)
	getAssignReq.Header.Set("Authorization", "Bearer ya29.pro_access_03")
	resp0, err := client.Do(getAssignReq)
	if err != nil {
		t.Fatal(err)
	}
	raw0, _ := io.ReadAll(resp0.Body)
	resp0.Body.Close()
	if !bytes.HasPrefix(raw0, []byte(models.ColabXSSIPrefix)) {
		t.Fatalf("expected XSSI prefix on /tun/m/assign GET, got %q", string(raw0))
	}
	var xsrf0 struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(pool.StripColabXSSI(raw0), &xsrf0)

	postBad, _ := http.NewRequest(http.MethodPost, srv.URL+"/tun/m/assign?nbh="+nbhBad+"&variant=GPU&accelerator=H100&authuser=0", nil)
	postBad.Header.Set("Authorization", "Bearer ya29.pro_access_03")
	postBad.Header.Set("X-Goog-Colab-Token", xsrf0.Token)
	respBad, err := client.Do(postBad)
	if err != nil {
		t.Fatal(err)
	}
	respBad.Body.Close()
	if respBad.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 on restricted H100 tier, got %d", respBad.StatusCode)
	}

	// 2. Allocate G4 VM on Ultra #1 via 2-step XSRF GET + POST /tun/m/assign
	nbh := models.NewNotebookHash()
	assignURL := srv.URL + "/tun/m/assign?nbh=" + nbh + "&variant=GPU&accelerator=G4&shape=hm&authuser=0"
	req1, _ := http.NewRequest(http.MethodGet, assignURL, nil)
	req1.Header.Set("Authorization", "Bearer ya29.ultra_access_01")
	r1, err := client.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := io.ReadAll(r1.Body)
	r1.Body.Close()
	var preAssign struct {
		Token string `json:"token"`
		Acc   string `json:"acc"`
	}
	_ = json.Unmarshal(pool.StripColabXSSI(b1), &preAssign)
	if preAssign.Acc != "G4" || preAssign.Token == "" {
		t.Fatalf("unexpected pre-assign response: %+v", preAssign)
	}

	req2, _ := http.NewRequest(http.MethodPost, assignURL, nil)
	req2.Header.Set("Authorization", "Bearer ya29.ultra_access_01")
	req2.Header.Set("X-Goog-Colab-Token", preAssign.Token)
	r2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("POST /tun/m/assign failed (%d): %s", r2.StatusCode, string(b2))
	}

	var assigned struct {
		Endpoint         string `json:"endpoint"`
		RuntimeProxyInfo struct {
			Token string `json:"token"`
			URL   string `json:"url"`
		} `json:"runtimeProxyInfo"`
	}
	_ = json.Unmarshal(pool.StripColabXSSI(b2), &assigned)
	if assigned.Endpoint == "" || assigned.RuntimeProxyInfo.Token == "" {
		t.Fatalf("invalid PostAssignmentResponse: %+v", assigned)
	}

	// 3. Keep-Alive Tunnel Ping (`X-Colab-Tunnel: Google`)
	kaReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/tun/m/"+assigned.Endpoint+"/keep-alive/?authuser=0", nil)
	kaReq.Header.Set("Authorization", "Bearer ya29.ultra_access_01")
	kaReq.Header.Set("X-Colab-Tunnel", "Google")
	kaResp, err := client.Do(kaReq)
	if err != nil || kaResp.StatusCode != http.StatusOK {
		t.Fatalf("keep-alive failed: %v", err)
	}
	kaResp.Body.Close()

	// 4. Out-of-band `/api/colab/resources` GPU VRAM & SM Telemetry
	resReq, _ := http.NewRequest(http.MethodGet, assigned.RuntimeProxyInfo.URL+"/api/colab/resources", nil)
	resReq.Header.Set("X-Colab-Runtime-Proxy-Token", assigned.RuntimeProxyInfo.Token)
	resResp, err := client.Do(resReq)
	if err != nil {
		t.Fatal(err)
	}
	resBody, _ := io.ReadAll(resResp.Body)
	resResp.Body.Close()
	if !strings.Contains(string(pool.StripColabXSSI(resBody)), "Blackwell") {
		t.Fatalf("expected Blackwell GPU in /api/colab/resources, got %s", string(resBody))
	}

	// 5. Jupyter `/api/contents` Upload + Download + `/api/kernels` Creation
	putBody, _ := json.Marshal(map[string]any{
		"format":  "base64",
		"content": base64.StdEncoding.EncodeToString([]byte("print('hello colabhive')\n")),
	})
	putReq, _ := http.NewRequest(http.MethodPut, assigned.RuntimeProxyInfo.URL+"/api/contents/content/train.py", bytes.NewReader(putBody))
	putReq.Header.Set("X-Colab-Runtime-Proxy-Token", assigned.RuntimeProxyInfo.Token)
	putResp, err := client.Do(putReq)
	if err != nil || putResp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT /api/contents failed: %v", err)
	}
	putResp.Body.Close()

	kernReq, _ := http.NewRequest(http.MethodPost, assigned.RuntimeProxyInfo.URL+"/api/kernels", strings.NewReader(`{"name":"python3"}`))
	kernReq.Header.Set("X-Colab-Runtime-Proxy-Token", assigned.RuntimeProxyInfo.Token)
	kernResp, err := client.Do(kernReq)
	if err != nil || kernResp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/kernels failed: %v", err)
	}
	var kern struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(kernResp.Body).Decode(&kern)
	kernResp.Body.Close()

	// 6. ZMQ-over-WebSocket Execution (stdout + matplotlib PNG display_data)
	wsURL := "ws" + strings.TrimPrefix(assigned.RuntimeProxyInfo.URL, "http") +
		"/api/kernels/" + kern.ID + "/channels?colab-runtime-proxy-token=" + assigned.RuntimeProxyInfo.Token
	wsConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WebSocket dial kernel channels failed: %v", err)
	}
	defer wsConn.Close()

	execMsg := map[string]any{
		"channel": "shell",
		"header": map[string]any{
			"msg_id":   "msg_test_01",
			"msg_type": "execute_request",
			"version":  "5.3",
		},
		"parent_header": map[string]any{},
		"metadata":      map[string]any{},
		"content": map[string]any{
			"code":          "import matplotlib.pyplot as plt\nprint('epoch 1 done')\nplt.show()",
			"silent":        false,
			"store_history": true,
		},
	}
	if err := wsConn.WriteJSON(execMsg); err != nil {
		t.Fatal(err)
	}

	sawStdout := false
	sawPlot := false
	sawIdleAfterReply := false
	sawReply := false
	_ = wsConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for !sawIdleAfterReply {
		var frame struct {
			Header  map[string]any `json:"header"`
			Content map[string]any `json:"content"`
		}
		if err := wsConn.ReadJSON(&frame); err != nil {
			t.Fatalf("ws read failed before idle: %v", err)
		}
		mt, _ := frame.Header["msg_type"].(string)
		switch mt {
		case "stream":
			if txt, _ := frame.Content["text"].(string); strings.Contains(txt, "epoch 1 done") {
				sawStdout = true
			}
		case "display_data":
			if data, ok := frame.Content["data"].(map[string]any); ok && data["image/png"] != nil {
				sawPlot = true
			}
		case "execute_reply":
			sawReply = true
		case "status":
			if st, _ := frame.Content["execution_state"].(string); st == "idle" && sawReply {
				sawIdleAfterReply = true
			}
		}
	}
	if !sawStdout || !sawPlot {
		t.Fatalf("expected both stdout and PNG display_data over ZMQ WebSocket (stdout=%v plot=%v)", sawStdout, sawPlot)
	}

	// 7. Unassign VM (`GET + POST /tun/m/unassign/<endpoint>`)
	unGet, _ := http.NewRequest(http.MethodGet, srv.URL+"/tun/m/unassign/"+assigned.Endpoint+"?authuser=0", nil)
	unGet.Header.Set("Authorization", "Bearer ya29.ultra_access_01")
	unR1, _ := client.Do(unGet)
	unB1, _ := io.ReadAll(unR1.Body)
	unR1.Body.Close()
	var unTok struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal(pool.StripColabXSSI(unB1), &unTok)

	unPost, _ := http.NewRequest(http.MethodPost, srv.URL+"/tun/m/unassign/"+assigned.Endpoint+"?authuser=0", nil)
	unPost.Header.Set("Authorization", "Bearer ya29.ultra_access_01")
	unPost.Header.Set("X-Goog-Colab-Token", unTok.Token)
	unR2, err := client.Do(unPost)
	if err != nil || unR2.StatusCode != http.StatusOK {
		t.Fatalf("POST /tun/m/unassign failed: %v", err)
	}
	unR2.Body.Close()
}

func TestSeedDemoClusterIdempotency(t *testing.T) {
	ctx := context.Background()
	st, err := store.OpenSQLite(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	srv := mockcolab.NewServer()
	defer srv.Close()

	hub := events.NewHub()
	poolSvc := pool.NewService(st, hub, srv.PoolConfig("demo-secret"))

	seeded, err := mockcolab.SeedDemoCluster(ctx, st, poolSvc, srv)
	if err != nil {
		t.Fatalf("SeedDemoCluster failed: %v", err)
	}
	if len(seeded.Accounts) != 4 || seeded.QuotaSummary == nil || seeded.QuotaSummary.AccountsTotal != 4 {
		t.Fatalf("unexpected seeded cluster: %+v", seeded)
	}

	// Verify contributor demo receipt works immediately via GetContributionByReceipt
	rcptStatus, err := poolSvc.GetContributionByReceipt(ctx, seeded.DemoReceiptToken)
	if err != nil || rcptStatus.Guardrails.MinReserveCU != 50.0 {
		t.Fatalf("GetContributionByReceipt on demo cluster failed: %+v (%v)", rcptStatus, err)
	}

	// Second call must be idempotent
	seeded2, err := mockcolab.SeedDemoCluster(ctx, st, poolSvc, srv)
	if err != nil || len(seeded2.Accounts) != 4 {
		t.Fatalf("expected idempotent 4 accounts on second SeedDemoCluster, got %+v (%v)", seeded2, err)
	}
}

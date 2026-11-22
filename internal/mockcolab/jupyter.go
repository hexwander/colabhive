package mockcolab

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/colabhive/colabhive/internal/models"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Tiny valid 1x1 transparent PNG for matplotlib / display_data tests.
const mockOnePixelPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

var wsUpgrader = websocket.Upgrader{
	Subprotocols: []string{"v1.kernel.websocket.jupyter.org"},
	CheckOrigin:  func(r *http.Request) bool { return true },
}

func (s *Server) authenticateRuntimeProxy(r *http.Request, endpoint string) (*mockVM, bool) {
	tok := strings.TrimSpace(r.Header.Get("X-Colab-Runtime-Proxy-Token"))
	if tok == "" {
		hdr := strings.TrimSpace(r.Header.Get("Authorization"))
		if strings.HasPrefix(strings.ToLower(hdr), "bearer ") {
			tok = strings.TrimSpace(hdr[7:])
		}
	}
	if tok == "" {
		tok = strings.TrimSpace(r.URL.Query().Get("colab-runtime-proxy-token"))
	}
	if tok == "" {
		tok = strings.TrimSpace(r.URL.Query().Get("token"))
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if endpoint != "" {
		vm, ok := s.vmsByEndpoint[endpoint]
		if !ok || (tok != "" && vm.ProxyToken != tok) || tok == "" {
			return nil, false
		}
		return vm, true
	}
	if tok != "" {
		vm, ok := s.proxyTokenToVM[tok]
		return vm, ok
	}
	return nil, false
}

func (s *Server) handleRuntimeSubtree(w http.ResponseWriter, r *http.Request) {
	// URL layout: /runtime/{endpoint}/api/...
	rest := strings.TrimPrefix(r.URL.Path, "/runtime/")
	slash := strings.IndexByte(rest, '/')
	if slash == -1 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "missing runtime subpath"})
		return
	}
	endpoint := rest[:slash]
	subPath := rest[slash:]
	vm, ok := s.authenticateRuntimeProxy(r, endpoint)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing X-Colab-Runtime-Proxy-Token"})
		return
	}
	s.dispatchVMRoute(vm, subPath, w, r)
}

func (s *Server) handleDirectRuntimeSubtree(w http.ResponseWriter, r *http.Request) {
	vm, ok := s.authenticateRuntimeProxy(r, "")
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or missing X-Colab-Runtime-Proxy-Token"})
		return
	}
	s.dispatchVMRoute(vm, r.URL.Path, w, r)
}

func (s *Server) dispatchVMRoute(vm *mockVM, subPath string, w http.ResponseWriter, r *http.Request) {
	switch {
	case subPath == "/api/colab/resources":
		s.handleVMResources(vm, w, r)
	case subPath == "/api/kernels":
		s.handleVMKernelsRoot(vm, w, r)
	case strings.HasPrefix(subPath, "/api/kernels/"):
		s.handleVMKernelItem(vm, strings.TrimPrefix(subPath, "/api/kernels/"), w, r)
	case subPath == "/api/sessions" || strings.HasPrefix(subPath, "/api/sessions/"):
		s.handleVMSessions(vm, w, r)
	case subPath == "/api/contents" || strings.HasPrefix(subPath, "/api/contents/"):
		s.handleVMContents(vm, strings.TrimPrefix(subPath, "/api/contents"), w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown jupyter runtime route " + subPath})
	}
}

func (s *Server) handleVMResources(vm *mockVM, w http.ResponseWriter, _ *http.Request) {
	spec, _ := models.GetHardwareSpec(vm.Accelerator)
	var gpus []map[string]any
	if spec.VRAMGB > 0 && spec.Variant == models.VariantGPU {
		totalBytes := int64(spec.VRAMGB) * 1024 * 1024 * 1024
		usedBytes := int64(float64(totalBytes) * 0.54)
		gpus = append(gpus, map[string]any{
			"name":              spec.DisplayName,
			"memoryUsedBytes":   usedBytes,
			"memoryTotalBytes":  totalBytes,
			"gpuUtilization":    0.92,
			"memoryUtilization": 0.54,
			"temperatureC":      66,
			"everUsed":          true,
		})
	}
	writeColabXSSIJSON(w, http.StatusOK, map[string]any{
		"memory": map[string]any{
			"totalBytes": int64(85899345920),
			"freeBytes":  int64(57982058496),
		},
		"disks": []map[string]any{
			{
				"filesystem": map[string]any{
					"label":      "kernel",
					"totalBytes": int64(253055090688),
					"usedBytes":  int64(42190848000),
				},
			},
		},
		"gpus": gpus,
	})
}

func (s *Server) handleVMKernelsRoot(vm *mockVM, w http.ResponseWriter, r *http.Request) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		out := make([]map[string]any, 0, len(vm.kernels))
		for _, k := range vm.kernels {
			out = append(out, kernelToWire(k))
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		kid := uuid.NewString()
		k := &mockKernel{
			ID:             kid,
			Name:           "python3",
			ExecutionState: "idle",
			LastActivity:   time.Now().UTC(),
		}
		vm.kernels[kid] = k
		writeJSON(w, http.StatusCreated, kernelToWire(k))

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (s *Server) handleVMKernelItem(vm *mockVM, rel string, w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(rel, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "missing kernel id"})
		return
	}
	kid := parts[0]

	if len(parts) >= 2 && parts[1] == "channels" {
		s.handleVMKernelWebSocket(vm, kid, w, r)
		return
	}

	vm.mu.Lock()
	k, ok := vm.kernels[kid]
	if !ok {
		vm.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "kernel not found"})
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			wire := kernelToWire(k)
			vm.mu.Unlock()
			writeJSON(w, http.StatusOK, wire)
		case http.MethodDelete:
			delete(vm.kernels, kid)
			vm.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			vm.mu.Unlock()
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		}
		return
	}

	action := parts[1]
	switch action {
	case "interrupt":
		k.Interrupted = true
		k.LastActivity = time.Now().UTC()
		vm.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case "restart":
		k.ExecutionCount = 0
		k.ExecutionState = "idle"
		k.LastActivity = time.Now().UTC()
		wire := kernelToWire(k)
		vm.mu.Unlock()
		writeJSON(w, http.StatusOK, wire)
	default:
		vm.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "unknown kernel action"})
	}
}

func kernelToWire(k *mockKernel) map[string]any {
	return map[string]any{
		"id":              k.ID,
		"name":            k.Name,
		"last_activity":   k.LastActivity.UTC().Format(time.RFC3339Nano),
		"execution_state": k.ExecutionState,
		"connections":     1,
	}
}

func (s *Server) handleVMSessions(vm *mockVM, w http.ResponseWriter, r *http.Request) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	switch r.Method {
	case http.MethodGet:
		out := make([]map[string]any, 0, len(vm.sessions))
		for _, sess := range vm.sessions {
			out = append(out, sess)
		}
		writeJSON(w, http.StatusOK, out)

	case http.MethodPost:
		var req struct {
			Path   string `json:"path"`
			Name   string `json:"name"`
			Type   string `json:"type"`
			Kernel struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"kernel"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		kid := req.Kernel.ID
		if kid == "" || vm.kernels[kid] == nil {
			kid = uuid.NewString()
			vm.kernels[kid] = &mockKernel{
				ID:             kid,
				Name:           "python3",
				ExecutionState: "idle",
				LastActivity:   time.Now().UTC(),
			}
		}
		sid := uuid.NewString()
		if req.Path == "" {
			req.Path = "empty.ipynb"
		}
		sessObj := map[string]any{
			"id":     sid,
			"path":   req.Path,
			"name":   req.Name,
			"type":   "notebook",
			"kernel": kernelToWire(vm.kernels[kid]),
		}
		vm.sessions[sid] = sessObj
		writeJSON(w, http.StatusCreated, sessObj)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func normalizeContentRelPath(raw string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(raw))
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." || cleaned == "" {
		return "content"
	}
	return cleaned
}

func (s *Server) handleVMContents(vm *mockVM, relPath string, w http.ResponseWriter, r *http.Request) {
	norm := normalizeContentRelPath(relPath)

	vm.mu.Lock()
	defer vm.mu.Unlock()

	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024))
		var putReq struct {
			Format  string `json:"format"`
			Content string `json:"content"`
			Chunk   *int   `json:"chunk"`
		}
		if err := json.Unmarshal(body, &putReq); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid PUT contents JSON"})
			return
		}
		var data []byte
		if strings.EqualFold(putReq.Format, "base64") {
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(putReq.Content))
			if err != nil {
				decoded, _ = base64.RawStdEncoding.DecodeString(strings.TrimSpace(putReq.Content))
			}
			data = decoded
		} else {
			data = []byte(putReq.Content)
		}

		if putReq.Chunk != nil && *putReq.Chunk != 1 {
			vm.files[norm] = append(vm.files[norm], data...)
		} else {
			vm.files[norm] = data
		}
		writeJSON(w, http.StatusCreated, map[string]any{
			"name": path.Base(norm),
			"path": norm,
			"type": "file",
			"size": len(vm.files[norm]),
		})

	case http.MethodGet:
		if data, isFile := vm.files[norm]; isFile {
			wantContent := r.URL.Query().Get("content") != "0"
			resp := map[string]any{
				"name":          path.Base(norm),
				"path":          norm,
				"type":          "file",
				"size":          len(data),
				"last_modified": time.Now().UTC().Format(time.RFC3339),
			}
			if wantContent {
				resp["format"] = "base64"
				resp["content"] = base64.StdEncoding.EncodeToString(data)
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}

		// Check directory children under norm + "/"
		prefix := norm + "/"
		childrenMap := make(map[string]map[string]any)
		for fPath, fData := range vm.files {
			if strings.HasPrefix(fPath, prefix) {
				rem := strings.TrimPrefix(fPath, prefix)
				slashIdx := strings.IndexByte(rem, '/')
				if slashIdx == -1 {
					childrenMap[rem] = map[string]any{
						"name": rem,
						"path": prefix + rem,
						"type": "file",
						"size": len(fData),
					}
				} else {
					dirName := rem[:slashIdx]
					if _, exists := childrenMap[dirName]; !exists {
						childrenMap[dirName] = map[string]any{
							"name": dirName,
							"path": prefix + dirName,
							"type": "directory",
							"size": 0,
						}
					}
				}
			}
		}

		if len(childrenMap) == 0 && norm != "content" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "path not found"})
			return
		}

		names := make([]string, 0, len(childrenMap))
		for n := range childrenMap {
			names = append(names, n)
		}
		sort.Strings(names)
		entries := make([]map[string]any, 0, len(names))
		for _, n := range names {
			entries = append(entries, childrenMap[n])
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"name":    path.Base(norm),
			"path":    norm,
			"type":    "directory",
			"content": entries,
		})

	case http.MethodDelete:
		if _, ok := vm.files[norm]; !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		delete(vm.files, norm)
		w.WriteHeader(http.StatusNoContent)

	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

type jupyterMessage struct {
	Channel      string         `json:"channel"`
	Header       map[string]any `json:"header"`
	ParentHeader map[string]any `json:"parent_header"`
	Metadata     map[string]any `json:"metadata"`
	Content      map[string]any `json:"content"`
}

func (s *Server) handleVMKernelWebSocket(vm *mockVM, kernelID string, w http.ResponseWriter, r *http.Request) {
	vm.mu.Lock()
	k := vm.kernels[kernelID]
	if k == nil {
		// Auto-materialize kernel if connected directly by ID
		k = &mockKernel{ID: kernelID, Name: "python3", ExecutionState: "idle", LastActivity: time.Now().UTC()}
		vm.kernels[kernelID] = k
	}
	vm.mu.Unlock()

	conn, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	isV1Binary := conn.Subprotocol() == "v1.kernel.websocket.jupyter.org"
	var writeMu sync.Mutex

	sendJupyter := func(channel, msgType string, parent map[string]any, metadata map[string]any, content map[string]any) error {
		if metadata == nil {
			metadata = map[string]any{}
		}
		if parent == nil {
			parent = map[string]any{}
		}
		hdr := map[string]any{
			"msg_id":   uuid.NewString(),
			"username": "colabhive",
			"session":  uuid.NewString(),
			"date":     time.Now().UTC().Format(time.RFC3339Nano),
			"msg_type": msgType,
			"version":  "5.3",
		}
		writeMu.Lock()
		defer writeMu.Unlock()

		if isV1Binary {
			frame := encodeJupyterV1Binary(channel, hdr, parent, metadata, content)
			return conn.WriteMessage(websocket.BinaryMessage, frame)
		}
		msg := jupyterMessage{
			Channel:      channel,
			Header:       hdr,
			ParentHeader: parent,
			Metadata:     metadata,
			Content:      content,
		}
		b, _ := json.Marshal(msg)
		return conn.WriteMessage(websocket.TextMessage, b)
	}

	for {
		mt, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		reqMsg, err := decodeJupyterIncoming(mt, raw, isV1Binary)
		if err != nil {
			continue
		}

		msgType, _ := reqMsg.Header["msg_type"].(string)
		switch msgType {
		case "kernel_info_request":
			_ = sendJupyter("iopub", "status", reqMsg.Header, nil, map[string]any{"execution_state": "busy"})
			_ = sendJupyter("shell", "kernel_info_reply", reqMsg.Header, nil, map[string]any{
				"status":           "ok",
				"protocol_version": "5.3",
				"language_info":    map[string]any{"name": "python", "version": "3.11.11"},
			})
			_ = sendJupyter("iopub", "status", reqMsg.Header, nil, map[string]any{"execution_state": "idle"})

		case "execute_request":
			code, _ := reqMsg.Content["code"].(string)
			silent, _ := reqMsg.Content["silent"].(bool)
			storeHistory := true
			if sh, ok := reqMsg.Content["store_history"].(bool); ok {
				storeHistory = sh
			}

			vm.mu.Lock()
			if !silent && storeHistory {
				k.ExecutionCount++
			}
			execCount := k.ExecutionCount
			k.ExecutionState = "busy"
			k.LastActivity = time.Now().UTC()
			isZombie := vm.ZombieWorker
			accSpec, _ := models.GetHardwareSpec(vm.Accelerator)
			vm.mu.Unlock()

			_ = sendJupyter("iopub", "status", reqMsg.Header, nil, map[string]any{"execution_state": "busy"})

			statusStr := "ok"
			switch {
			case strings.Contains(code, "drive.mount") || strings.Contains(code, "authenticate_user"):
				// Emit Colab credential-propagation request so Contributor Firewall can intercept and block!
				_ = sendJupyter("iopub", "colab_request", reqMsg.Header, map[string]any{
					"colab_request_type": "request_auth",
					"colab_msg_id":       "colab_auth_msg_01",
				}, map[string]any{
					"request": map[string]any{"authType": "dfs_ephemeral"},
				})

			case strings.Contains(code, "ZeroDivisionError") || strings.Contains(code, "1/0") || strings.Contains(code, "raise RuntimeError"):
				statusStr = "error"
				_ = sendJupyter("iopub", "error", reqMsg.Header, nil, map[string]any{
					"ename":     "ZeroDivisionError",
					"evalue":    "division by zero",
					"traceback": []string{"Traceback (most recent call last):", "ZeroDivisionError: division by zero"},
				})

			case strings.Contains(code, "sys.exit("):
				statusStr = "error"
				exitVal := "1"
				if idx := strings.Index(code, "sys.exit("); idx != -1 {
					sub := code[idx+9:]
					if rp := strings.IndexByte(sub, ')'); rp != -1 {
						exitVal = strings.TrimSpace(sub[:rp])
					}
				}
				_ = sendJupyter("iopub", "error", reqMsg.Header, nil, map[string]any{
					"ename":     "SystemExit",
					"evalue":    exitVal,
					"traceback": []string{"SystemExit: " + exitVal},
				})

			case strings.Contains(code, "probe_worker") || strings.Contains(code, "/proc/"):
				stateCode := "R"
				alive := true
				if isZombie {
					stateCode = "Z"
					alive = false
				}
				probeJSON, _ := json.Marshal(map[string]any{
					"pid":             4812,
					"state":           stateCode,
					"alive":           alive,
					"new_offset":      256,
					"log_delta":       "[shard] step=10 loss=0.312\n",
					"has_fatal_trace": false,
				})
				_ = sendJupyter("iopub", "stream", reqMsg.Header, nil, map[string]any{
					"name": "stdout",
					"text": string(probeJSON) + "\n",
				})

			default:
				if !silent {
					if strings.Contains(code, "matplotlib") || strings.Contains(code, "plt.show") || strings.Contains(code, "__MOCK_PLOT__") {
						_ = sendJupyter("iopub", "display_data", reqMsg.Header, nil, map[string]any{
							"data": map[string]any{
								"image/png":  mockOnePixelPNGB64,
								"text/plain": "<Figure size 640x480 with 1 Axes>",
							},
							"metadata": map[string]any{},
						})
					}
					stdoutText := simulateStdoutForCode(code, accSpec)
					if stdoutText != "" {
						_ = sendJupyter("iopub", "stream", reqMsg.Header, nil, map[string]any{
							"name": "stdout",
							"text": stdoutText,
						})
					}
					if exprRes := simulateResultForCode(code, accSpec); exprRes != "" {
						_ = sendJupyter("iopub", "execute_result", reqMsg.Header, nil, map[string]any{
							"execution_count": execCount,
							"data":            map[string]any{"text/plain": exprRes},
							"metadata":        map[string]any{},
						})
					}
				}
			}

			_ = sendJupyter("shell", "execute_reply", reqMsg.Header, nil, map[string]any{
				"status":          statusStr,
				"execution_count": execCount,
			})

			vm.mu.Lock()
			k.ExecutionState = "idle"
			vm.mu.Unlock()
			_ = sendJupyter("iopub", "status", reqMsg.Header, nil, map[string]any{"execution_state": "idle"})
		}
	}
}

func simulateStdoutForCode(code string, spec models.HardwareSpec) string {
	trimmed := strings.TrimSpace(code)
	if strings.Contains(trimmed, "os.makedirs('/content'") && !strings.Contains(trimmed, "print(") {
		return ""
	}
	if strings.Contains(trimmed, "uv") && strings.Contains(trimmed, "pip") {
		return "Resolved packages in 84ms\nInstalled packages via uv!\n"
	}
	if strings.Contains(trimmed, "nvidia-smi") {
		return fmt.Sprintf("%s, %d MiB, 92 %%, 65 C\n", spec.DisplayName, spec.VRAMGB*1024)
	}
	if strings.Contains(trimmed, "get_device_name") {
		return spec.DisplayName + "\n"
	}
	// Extract literal inside print("...") or print('...')
	if idx := strings.Index(trimmed, "print("); idx != -1 {
		sub := trimmed[idx+6:]
		if rp := strings.IndexByte(sub, ')'); rp != -1 {
			arg := strings.Trim(strings.TrimSpace(sub[:rp]), `"'`)
			return arg + "\n"
		}
	}
	return ""
}

func simulateResultForCode(code string, spec models.HardwareSpec) string {
	trimmed := strings.TrimSpace(code)
	if trimmed == "1 + 1" || trimmed == "1+1" {
		return "2"
	}
	if strings.Contains(trimmed, "torch.cuda.is_available()") && !strings.Contains(trimmed, "print(") {
		if spec.Variant == models.VariantGPU {
			return "True"
		}
		return "False"
	}
	return ""
}

func decodeJupyterIncoming(mt int, raw []byte, isV1Binary bool) (*jupyterMessage, error) {
	if mt == websocket.TextMessage {
		var m jupyterMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return &m, nil
	}
	if isV1Binary && len(raw) >= 16 {
		nOffsets := int(binary.LittleEndian.Uint64(raw[:8]))
		if nOffsets >= 6 && len(raw) >= 8*(nOffsets+1) {
			offs := make([]uint64, nOffsets)
			for i := 0; i < nOffsets; i++ {
				offs[i] = binary.LittleEndian.Uint64(raw[8*(i+1) : 8*(i+2)])
			}
			part := func(idx int) []byte {
				start := offs[idx]
				end := uint64(len(raw))
				if idx+1 < nOffsets {
					end = offs[idx+1]
				}
				if start > end || end > uint64(len(raw)) {
					return nil
				}
				return raw[start:end]
			}
			var m jupyterMessage
			m.Channel = string(part(0))
			_ = json.Unmarshal(part(1), &m.Header)
			_ = json.Unmarshal(part(2), &m.ParentHeader)
			_ = json.Unmarshal(part(3), &m.Metadata)
			_ = json.Unmarshal(part(4), &m.Content)
			return &m, nil
		}
	}
	// Default binary frame (big-endian uint32 offset table, Buffer 0 = JSON envelope)
	if len(raw) >= 12 {
		nbufs := int(binary.BigEndian.Uint32(raw[:4]))
		if nbufs >= 1 && len(raw) >= 4*(nbufs+1) {
			start := binary.BigEndian.Uint32(raw[4:8])
			end := uint32(len(raw))
			if nbufs >= 2 {
				end = binary.BigEndian.Uint32(raw[8:12])
			}
			if start <= end && int(end) <= len(raw) {
				var m jupyterMessage
				if err := json.Unmarshal(raw[start:end], &m); err == nil {
					return &m, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("unsupported jupyter frame")
}

func encodeJupyterV1Binary(channel string, hdr, parent, meta, content map[string]any) []byte {
	chB := []byte(channel)
	hB, _ := json.Marshal(hdr)
	pB, _ := json.Marshal(parent)
	mB, _ := json.Marshal(meta)
	cB, _ := json.Marshal(content)

	parts := [][]byte{chB, hB, pB, mB, cB}
	nOffsets := len(parts) + 1
	headerLen := 8 * (1 + nOffsets)
	totalLen := headerLen
	for _, p := range parts {
		totalLen += len(p)
	}

	buf := make([]byte, totalLen)
	binary.LittleEndian.PutUint64(buf[0:8], uint64(nOffsets))
	cur := uint64(headerLen)
	for i, p := range parts {
		binary.LittleEndian.PutUint64(buf[8*(i+1):8*(i+2)], cur)
		copy(buf[cur:], p)
		cur += uint64(len(p))
	}
	binary.LittleEndian.PutUint64(buf[8*nOffsets:8*(nOffsets+1)], cur)
	return buf
}

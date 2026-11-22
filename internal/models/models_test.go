package models

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHardwareCatalogAndShapeRules(t *testing.T) {
	if len(HardwareCatalog) != 8 {
		t.Fatalf("expected 8 hardware specs, got %d", len(HardwareCatalog))
	}

	// L4, V5E1, V6E1 must omit shape=hm on /tun/m/assign
	for _, acc := range []AcceleratorType{AcceleratorL4, AcceleratorV5E1, AcceleratorV6E1} {
		if got := ResolveAssignShapeParam(acc, true); got != "" {
			t.Errorf("expected empty shape param for %s, got %q", acc, got)
		}
		if shape := EffectiveMachineShape(acc, false); shape != ShapeHighRAM {
			t.Errorf("expected %s to always resolve to HIGH_RAM, got %s", acc, shape)
		}
	}

	// G4, H100, A100, T4, CPU must emit shape=hm when highMem=true
	for _, acc := range []AcceleratorType{AcceleratorG4, AcceleratorH100, AcceleratorA100, AcceleratorT4, AcceleratorCPU} {
		if got := ResolveAssignShapeParam(acc, true); got != "hm" {
			t.Errorf("expected shape=hm for %s, got %q", acc, got)
		}
	}
}

func TestNotebookHashAndAttachURL(t *testing.T) {
	nbh := NewNotebookHash()
	if len(nbh) != 44 {
		t.Fatalf("expected 44-char nbh, got %d (%q)", len(nbh), nbh)
	}
	if !strings.HasSuffix(nbh, "........") {
		t.Errorf("expected 8 trailing dots in nbh, got %q", nbh)
	}
	if strings.Contains(nbh, "-") {
		t.Errorf("nbh must replace hyphens with underscores: %q", nbh)
	}

	url := BuildColabWebAttachURL("gpu-g4-s-2k8m9p1x4z7q")
	if !strings.Contains(url, "?dbu=%2Ftun%2Fm%2Fgpu-g4-s-2k8m9p1x4z7q#datalabBackendUrl=") {
		t.Errorf("unexpected attach URL: %s", url)
	}
}

func TestSecretFieldsExcludedFromJSON(t *testing.T) {
	acc := Account{
		ID:                    "acc_test",
		Email:                 "alice@example.com",
		EncryptedRefreshToken: "secret_refresh",
		EncryptedAccessToken:  "secret_access",
		OAuthClientSecret:     "secret_client",
		ReceiptTokenHash:      "secret_hash",
	}
	b, err := json.Marshal(acc)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, forbidden := range []string{"secret_refresh", "secret_access", "secret_client", "secret_hash"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("Account JSON leaked secret %q: %s", forbidden, s)
		}
	}

	sess := RuntimeSession{
		ID:         "sess_1",
		ProxyToken: "secret_proxy_capability_token",
	}
	sb, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sb), "secret_proxy_capability_token") {
		t.Fatalf("RuntimeSession JSON leaked ProxyToken: %s", string(sb))
	}
}

func TestNormalizeAcceleratorWireVariantsAndTPUFallback(t *testing.T) {
	cases := map[string]AcceleratorType{
		"NVIDIA_G4":        AcceleratorG4,
		"ACCELERATOR_H100": AcceleratorH100,
		"TESLA_T4":         AcceleratorT4,
		"TPU_V6E_1":        AcceleratorV6E1,
		"v6e-1":            AcceleratorV6E1,
		"TPU-V5E-1":        AcceleratorV5E1,
		"v5e_1":            AcceleratorV5E1,
		"VARIANT_CPU":      AcceleratorCPU,
	}
	for raw, want := range cases {
		got, err := NormalizeAccelerator(raw)
		if err != nil || got != want {
			t.Fatalf("NormalizeAccelerator(%q) = (%q, %v), want (%q, nil)", raw, got, err, want)
		}
	}

	// Verify V6E1 -> V5E1 fallback works even when DefaultGPUFallbackChain is supplied from RouterPolicy!
	tpuCandidates := FallbackCandidatesFrom(AcceleratorV6E1, true, DefaultGPUFallbackChain)
	if len(tpuCandidates) != 2 || tpuCandidates[0] != AcceleratorV6E1 || tpuCandidates[1] != AcceleratorV5E1 {
		t.Fatalf("expected [V6E1, V5E1] TPU fallback candidates, got %v", tpuCandidates)
	}
}

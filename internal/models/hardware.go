// Package models defines the canonical domain entities, protocol wire contracts,
// hardware accelerator catalog, and decoupled service interfaces for ColabHive.
//
// All backend packages (store, pool/auth, runtime/jupyter, mcp, router, server, cli)
// depend exclusively on this package to guarantee zero circular imports.
package models

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// AcceleratorType identifies a Google Colab compute accelerator tier.
type AcceleratorType string

const (
	AcceleratorG4   AcceleratorType = "G4"   // NVIDIA RTX PRO 6000 Blackwell (96 GB GDDR7, SM 12.0)
	AcceleratorH100 AcceleratorType = "H100" // NVIDIA H100 SXM5 (80 GB HBM3, SM 9.0)
	AcceleratorA100 AcceleratorType = "A100" // NVIDIA A100 SXM4 (40/80 GB HBM2e, SM 8.0)
	AcceleratorL4   AcceleratorType = "L4"   // NVIDIA L4 Ada (24 GB GDDR6, SM 8.9)
	AcceleratorT4   AcceleratorType = "T4"   // NVIDIA Tesla T4 Turing (16 GB GDDR6, SM 7.5)
	AcceleratorV6E1 AcceleratorType = "V6E1" // Cloud TPU v6e-1 Trillium (32 GB HBM)
	AcceleratorV5E1 AcceleratorType = "V5E1" // Cloud TPU v5e-1 (16 GB HBM)
	AcceleratorCPU  AcceleratorType = "CPU"  // Standard / High-RAM x86_64 CPU VM ("NONE" on Colab wire)
)

// AllAccelerators lists all supported ColabHive compute tiers in flagship display order.
var AllAccelerators = []AcceleratorType{
	AcceleratorG4,
	AcceleratorH100,
	AcceleratorA100,
	AcceleratorL4,
	AcceleratorT4,
	AcceleratorV6E1,
	AcceleratorV5E1,
	AcceleratorCPU,
}

// RuntimeVariant represents the Colab runtime family.
type RuntimeVariant string

const (
	VariantDefault RuntimeVariant = "DEFAULT" // Wire integer 0 (CPU)
	VariantGPU     RuntimeVariant = "GPU"     // Wire integer 1
	VariantTPU     RuntimeVariant = "TPU"     // Wire integer 2
)

// WireCode returns the integer enum used in /tun/m/assignments JSON payloads.
func (v RuntimeVariant) WireCode() int {
	switch v {
	case VariantGPU:
		return 1
	case VariantTPU:
		return 2
	default:
		return 0
	}
}

// PublicAPIVariant returns the colaboratory.googleapis.com v1beta variant enum string.
func (v RuntimeVariant) PublicAPIVariant() string {
	switch v {
	case VariantGPU:
		return "VARIANT_GPU"
	case VariantTPU:
		return "VARIANT_TPU"
	default:
		return "VARIANT_CPU"
	}
}

// VariantFromWireCode converts a /tun/m/assignments integer variant into RuntimeVariant.
func VariantFromWireCode(code int) RuntimeVariant {
	switch code {
	case 1:
		return VariantGPU
	case 2:
		return VariantTPU
	default:
		return VariantDefault
	}
}

// MachineShape represents the host RAM configuration of a Colab VM.
type MachineShape string

const (
	ShapeStandard MachineShape = "STANDARD" // Wire integer 0 ("SHAPE_STANDARD")
	ShapeHighRAM  MachineShape = "HIGH_RAM" // Wire integer 1 ("SHAPE_HIGHMEM" / query shape=hm)
)

// ShapeFromWireCode converts a /tun/m/assignments integer machineShape into MachineShape.
func ShapeFromWireCode(code int) MachineShape {
	if code == 1 {
		return ShapeHighRAM
	}
	return ShapeStandard
}

// PublicAPIShape returns the colaboratory.googleapis.com v1beta shape enum string.
func (s MachineShape) PublicAPIShape() string {
	if s == ShapeHighRAM {
		return "SHAPE_HIGHMEM"
	}
	return "SHAPE_STANDARD"
}

// RateCalibrationSource indicates how a CU/hr burn rate was determined.
type RateCalibrationSource string

const (
	CalibrationLive      RateCalibrationSource = "live"      // Measured live from ccu-info / v1/user-info
	CalibrationEmpirical RateCalibrationSource = "empirical" // Historical EWMA from local pool ledger
	CalibrationReference RateCalibrationSource = "reference" // Built-in Colab 2026 baseline constant
)

// HardwareSpec defines static capabilities, wire encodings, and reference burn rates for a tier.
type HardwareSpec struct {
	Accelerator         AcceleratorType `json:"accelerator"`
	WireAccelerator     string          `json:"wire_accelerator"` // "NONE" for CPU, otherwise matches Accelerator
	DisplayName         string          `json:"display_name"`
	ShortLabel          string          `json:"short_label"`
	Architecture        string          `json:"architecture"`
	Variant             RuntimeVariant  `json:"variant"`
	VRAMGB              int             `json:"vram_gb"`
	DefaultBurnRateCUHr float64         `json:"default_burn_rate_cu_hr"`
	MinBurnRateCUHr     float64         `json:"min_burn_rate_cu_hr"`
	MaxBurnRateCUHr     float64         `json:"max_burn_rate_cu_hr"`
	FreeTierEligible    bool            `json:"free_tier_eligible"`
	SupportsHighMemFlag bool            `json:"supports_high_mem_flag"` // False for L4/V5E1/V6E1 (shape=hm must be omitted)
	AlwaysHighMem       bool            `json:"always_high_mem"`        // True for L4/V5E1/V6E1
	OptimalWorkload     string          `json:"optimal_workload"`
	CLIFlag             string          `json:"cli_flag"`
	FallbackOrder       int             `json:"fallback_order"`
}

// HardwareCatalog contains the authoritative Colab hardware definitions.
var HardwareCatalog = []HardwareSpec{
	{
		Accelerator:         AcceleratorG4,
		WireAccelerator:     "G4",
		DisplayName:         "NVIDIA RTX PRO 6000 Blackwell",
		ShortLabel:          "G4 96GB",
		Architecture:        "Blackwell · SM 12.0 · GDDR7",
		Variant:             VariantGPU,
		VRAMGB:              96,
		DefaultBurnRateCUHr: 9.66,
		MinBurnRateCUHr:     6.70,
		MaxBurnRateCUHr:     10.00,
		FreeTierEligible:    false,
		SupportsHighMemFlag: true,
		AlwaysHighMem:       false,
		OptimalWorkload:     ">=26B BF16 Models (Qwen-27B / Gemma-26B)",
		CLIFlag:             "--gpu G4",
		FallbackOrder:       2,
	},
	{
		Accelerator:         AcceleratorH100,
		WireAccelerator:     "H100",
		DisplayName:         "NVIDIA H100 SXM5",
		ShortLabel:          "H100 80GB",
		Architecture:        "Hopper · SM 9.0 · HBM3",
		Variant:             VariantGPU,
		VRAMGB:              80,
		DefaultBurnRateCUHr: 12.50,
		MinBurnRateCUHr:     12.50,
		MaxBurnRateCUHr:     18.00,
		FreeTierEligible:    false,
		SupportsHighMemFlag: true,
		AlwaysHighMem:       false,
		OptimalWorkload:     "FP8 / FlashAttention-3 / Dense Training",
		CLIFlag:             "--gpu H100",
		FallbackOrder:       1,
	},
	{
		Accelerator:         AcceleratorA100,
		WireAccelerator:     "A100",
		DisplayName:         "NVIDIA A100 SXM4",
		ShortLabel:          "A100 40/80GB",
		Architecture:        "Ampere · SM 8.0 · HBM2e",
		Variant:             VariantGPU,
		VRAMGB:              40,
		DefaultBurnRateCUHr: 8.88,
		MinBurnRateCUHr:     7.00,
		MaxBurnRateCUHr:     10.60,
		FreeTierEligible:    false,
		SupportsHighMemFlag: true,
		AlwaysHighMem:       false,
		OptimalWorkload:     "<=14B BF16 · Native Triton / CUDA Wheels",
		CLIFlag:             "--gpu A100 --high-mem",
		FallbackOrder:       3,
	},
	{
		Accelerator:         AcceleratorL4,
		WireAccelerator:     "L4",
		DisplayName:         "NVIDIA L4",
		ShortLabel:          "L4 24GB",
		Architecture:        "Ada Lovelace · SM 8.9 · GDDR6",
		Variant:             VariantGPU,
		VRAMGB:              24,
		DefaultBurnRateCUHr: 4.52,
		MinBurnRateCUHr:     1.80,
		MaxBurnRateCUHr:     4.60,
		FreeTierEligible:    false,
		SupportsHighMemFlag: false, // Omit shape=hm on /tun/m/assign
		AlwaysHighMem:       true,
		OptimalWorkload:     "7B-9B Inference / LoRA / Diffusion",
		CLIFlag:             "--gpu L4",
		FallbackOrder:       4,
	},
	{
		Accelerator:         AcceleratorT4,
		WireAccelerator:     "T4",
		DisplayName:         "NVIDIA Tesla T4",
		ShortLabel:          "T4 16GB",
		Architecture:        "Turing · SM 7.5 · GDDR6",
		Variant:             VariantGPU,
		VRAMGB:              16,
		DefaultBurnRateCUHr: 1.96,
		MinBurnRateCUHr:     0.00,
		MaxBurnRateCUHr:     2.00,
		FreeTierEligible:    true,
		SupportsHighMemFlag: true,
		AlwaysHighMem:       false,
		OptimalWorkload:     "<=4B Eval / Unit Tests / Free Dynamic Window",
		CLIFlag:             "--gpu T4",
		FallbackOrder:       5,
	},
	{
		Accelerator:         AcceleratorV6E1,
		WireAccelerator:     "V6E1",
		DisplayName:         "Cloud TPU v6e-1 Trillium",
		ShortLabel:          "TPU v6e-1 32GB",
		Architecture:        "Trillium · 1 Chip · HBM",
		Variant:             VariantTPU,
		VRAMGB:              32,
		DefaultBurnRateCUHr: 5.30,
		MinBurnRateCUHr:     4.00,
		MaxBurnRateCUHr:     5.50,
		FreeTierEligible:    false,
		SupportsHighMemFlag: false, // Omit shape=hm on /tun/m/assign
		AlwaysHighMem:       true,
		OptimalWorkload:     "JAX / XLA / PyTorch-XLA High Perf/CU",
		CLIFlag:             "--tpu v6e1",
		FallbackOrder:       6,
	},
	{
		Accelerator:         AcceleratorV5E1,
		WireAccelerator:     "V5E1",
		DisplayName:         "Cloud TPU v5e-1",
		ShortLabel:          "TPU v5e-1 16GB",
		Architecture:        "TPU v5e · 1 Chip · HBM",
		Variant:             VariantTPU,
		VRAMGB:              16,
		DefaultBurnRateCUHr: 3.85,
		MinBurnRateCUHr:     2.50,
		MaxBurnRateCUHr:     4.00,
		FreeTierEligible:    false,
		SupportsHighMemFlag: false, // Omit shape=hm on /tun/m/assign
		AlwaysHighMem:       true,
		OptimalWorkload:     "Cost-Efficient JAX / Embedding Extraction",
		CLIFlag:             "--tpu v5e1",
		FallbackOrder:       7,
	},
	{
		Accelerator:         AcceleratorCPU,
		WireAccelerator:     "NONE",
		DisplayName:         "Cloud x86_64 CPU VM",
		ShortLabel:          "CPU VM",
		Architecture:        "x86_64 Cloud Host",
		Variant:             VariantDefault,
		VRAMGB:              0,
		DefaultBurnRateCUHr: 0.74,
		MinBurnRateCUHr:     0.00,
		MaxBurnRateCUHr:     1.10,
		FreeTierEligible:    true,
		SupportsHighMemFlag: true,
		AlwaysHighMem:       false,
		OptimalWorkload:     "Data Preprocessing / Aggregation (+ Free Tier)",
		CLIFlag:             "",
		FallbackOrder:       8,
	},
}

var hardwareByType = func() map[AcceleratorType]HardwareSpec {
	m := make(map[AcceleratorType]HardwareSpec, len(HardwareCatalog))
	for _, spec := range HardwareCatalog {
		m[spec.Accelerator] = spec
	}
	return m
}()

// DefaultGPUFallbackChain is the general-purpose auto-tier waterfall on HTTP 400/503.
var DefaultGPUFallbackChain = []AcceleratorType{
	AcceleratorH100,
	AcceleratorG4,
	AcceleratorA100,
	AcceleratorL4,
	AcceleratorT4,
}

// LargeVRAMFallbackChain prevents >=26B workloads from silently dropping onto <=40GB GPUs.
var LargeVRAMFallbackChain = []AcceleratorType{
	AcceleratorH100,
	AcceleratorG4,
}

// NormalizeAccelerator parses CLI/API/wire strings ("g4", "NVIDIA_G4", "TPU_V6E_1", "NONE", "") into canonical AcceleratorType.
func NormalizeAccelerator(raw string) (AcceleratorType, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(raw))
	trimmed = strings.ReplaceAll(trimmed, "-", "_")
	for _, pfx := range []string{"ACCELERATOR_TYPE_", "ACCELERATOR_", "NVIDIA_", "TESLA_"} {
		trimmed = strings.TrimPrefix(trimmed, pfx)
	}
	switch trimmed {
	case "", "NONE", "CPU", "DEFAULT", "VARIANT_CPU":
		return AcceleratorCPU, nil
	case "G4", "RTX_PRO_6000":
		return AcceleratorG4, nil
	case "H100":
		return AcceleratorH100, nil
	case "A100":
		return AcceleratorA100, nil
	case "L4":
		return AcceleratorL4, nil
	case "T4":
		return AcceleratorT4, nil
	case "V6E1", "V6E_1", "TPU_V6E1", "TPU_V6E_1", "V6E", "TPU_V6E":
		return AcceleratorV6E1, nil
	case "V5E1", "V5E_1", "TPU_V5E1", "TPU_V5E_1", "V5E", "TPU_V5E":
		return AcceleratorV5E1, nil
	default:
		return "", fmt.Errorf("unsupported accelerator %q (valid: G4, H100, A100, L4, T4, V6E1, V5E1, CPU)", raw)
	}
}

// GetHardwareSpec returns the canonical HardwareSpec for an AcceleratorType.
func GetHardwareSpec(acc AcceleratorType) (HardwareSpec, bool) {
	spec, ok := hardwareByType[acc]
	return spec, ok
}

// ResolveAssignShapeParam returns "hm" if and only if highMem is requested AND the accelerator
// supports an explicit shape=hm query parameter on /tun/m/assign (omits for L4, V5E1, V6E1).
func ResolveAssignShapeParam(acc AcceleratorType, highMem bool) string {
	if !highMem {
		return ""
	}
	spec, ok := hardwareByType[acc]
	if !ok || !spec.SupportsHighMemFlag {
		return ""
	}
	return "hm"
}

// EffectiveMachineShape resolves the resulting MachineShape for a given accelerator and highMem flag.
func EffectiveMachineShape(acc AcceleratorType, highMem bool) MachineShape {
	spec, ok := hardwareByType[acc]
	if ok && spec.AlwaysHighMem {
		return ShapeHighRAM
	}
	if highMem {
		return ShapeHighRAM
	}
	return ShapeStandard
}

// FallbackCandidatesFrom returns the ordered slice of accelerators starting at requested
// along the given chain (or just [requested] if autoFallback is false).
func FallbackCandidatesFrom(requested AcceleratorType, autoFallback bool, chain []AcceleratorType) []AcceleratorType {
	if !autoFallback {
		return []AcceleratorType{requested}
	}
	if len(chain) == 0 {
		chain = DefaultGPUFallbackChain
	}
	for i, item := range chain {
		if item == requested {
			out := make([]AcceleratorType, len(chain[i:]))
			copy(out, chain[i:])
			return out
		}
	}
	if requested == AcceleratorV6E1 {
		return []AcceleratorType{AcceleratorV6E1, AcceleratorV5E1}
	}
	return []AcceleratorType{requested}
}

// NewNotebookHash generates the 44-character web-safe notebook hash (`nbh`) required by
// Colab's /tun/m/assign endpoint (UUIDv4 with underscores + 8 trailing dots).
func NewNotebookHash() string {
	s := strings.ReplaceAll(uuid.NewString(), "-", "_")
	if len(s) < 44 {
		s += strings.Repeat(".", 44-len(s))
	}
	return s
}

// NewPrefixedID mints a collision-resistant identifier with a domain prefix (e.g. "acc_", "sess_", "ch_inv_").
func NewPrefixedID(prefix string) string {
	raw := strings.ReplaceAll(uuid.NewString(), "-", "")
	return prefix + raw[:16]
}

// BuildColabWebAttachURL constructs the exact Colab Web UI URL that attaches to an existing
// headless endpoint without allocating a duplicate runtime.
func BuildColabWebAttachURL(endpoint string) string {
	const host = "https://colab.research.google.com"
	return fmt.Sprintf(
		"%s/notebooks/empty.ipynb?dbu=%%2Ftun%%2Fm%%2F%s#datalabBackendUrl=%s/tun/m/%s",
		host, endpoint, host, endpoint,
	)
}

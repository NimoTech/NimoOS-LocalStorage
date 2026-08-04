package mdadm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveMdadmPath(t *testing.T) {
	t.Run("defaults to the distribution binary on PATH", func(t *testing.T) {
		t.Setenv("MDADM_PATH", "")
		if got := resolveMdadmPath(); got != DefaultMdadmPath {
			t.Fatalf("resolveMdadmPath() = %q, want %q", got, DefaultMdadmPath)
		}
	})

	t.Run("MDADM_PATH overrides it", func(t *testing.T) {
		t.Setenv("MDADM_PATH", "/opt/custom/mdadm")
		if got := resolveMdadmPath(); got != "/opt/custom/mdadm" {
			t.Fatalf("resolveMdadmPath() = %q, want the MDADM_PATH value", got)
		}
	})
}

func TestCheckSupport(t *testing.T) {
	t.Run("reports a missing binary with an actionable message", func(t *testing.T) {
		orig := MdadmPath
		t.Cleanup(func() { MdadmPath = orig })
		MdadmPath = filepath.Join(t.TempDir(), "mdadm-not-installed")

		err := CheckSupport()
		if err == nil {
			t.Fatal("CheckSupport() = nil, want an error when the binary is absent")
		}
		if !strings.Contains(err.Error(), "please install mdadm") {
			t.Fatalf("CheckSupport() error = %q, want it to say how to fix it", err)
		}
	})

	t.Run("accepts an executable that exists", func(t *testing.T) {
		orig := MdadmPath
		t.Cleanup(func() { MdadmPath = orig })

		bin := filepath.Join(t.TempDir(), "mdadm")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatalf("write fake binary: %v", err)
		}
		MdadmPath = bin

		if err := CheckSupport(); err != nil {
			t.Fatalf("CheckSupport() = %v, want nil for an existing executable", err)
		}
	})
}

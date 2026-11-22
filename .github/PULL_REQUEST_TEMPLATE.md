## Summary

<!-- Briefly describe what this PR changes and why. Link any related issue (e.g., Closes #123). -->

## Changes Made

- 
- 

## Component Checklist

- [ ] **Zero Live Credentials / PII**: Confirmed zero real OAuth tokens (`1//0...`, `ya29...`), `.master.key` files, `.chvault` bundles, or personal email addresses are included.
- [ ] **Zero Live Compute Burn in Tests**: All new tests run hermetically against mock servers (`httptest.Server` / `mockcolab`) without allocating real Colab VMs.
- [ ] **Security & Isolation Invariants**: Preserved AES-256-GCM vault encryption, per-account proxy egress pinning, Contributor Drive Firewall (`BlockDrivePropagation`), and surgical `.bak` backups on `colabhive connect`.
- [ ] **Bilingual & Design System Parity** *(if touching `web/`)*: Updated both `zh-CN` and `en-US` strings and adhered to the Precision Compute Observatory palette (`tabular-nums`, crisp `1px` borders, zero purple gradients).

## Verification

```bash
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test -count=1 ./...
pnpm --dir web run build
PYTHONPATH=sdk/python/src python -m unittest discover -s sdk/python/tests -v
```

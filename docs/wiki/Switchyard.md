# NVIDIA NeMo Switchyard

Switchyard is a first-class but optional model-routing backend.

The architecture boundary is deliberate:

```text
HarnessMesh
  |
  | chooses participant / harness
  v
Codex / Claude / compatible harness
  |
  | optional
  v
Switchyard
  |
  | chooses model / provider
  v
LLM
```

HarnessMesh must not duplicate Switchyard's model-tier or provider-selection logic.

## Diagnostics

```bash
bin/harnessmesh switchyard doctor --config <config>
bin/harnessmesh switchyard routes --config <config>
bin/harnessmesh switchyard config validate --config <config>
```

HarnessMesh also supports fixed and external model-routing modes for harnesses where Switchyard is disabled, unnecessary, or technically unavailable.

See [docs/switchyard.md](../switchyard.md) for the detailed integration guide.

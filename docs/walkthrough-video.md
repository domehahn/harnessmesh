# HarnessMesh Walkthrough-Video

The repository includes a reproducible CLI/UI walkthrough recording. It uses
the real React console with deterministic dashboard responses and a curated
terminal introduction, so it is safe to run without provider credentials.

Start the frontend in one terminal:

```bash
cd frontend
npm run dev -- --host 127.0.0.1
```

Then, in a second terminal:

```bash
cd frontend
npm run demo:video
```

The reference video is committed at `artifacts/videos/harnessmesh-walkthrough.webm`.
Running the command regenerates and overwrites that local artifact.
Override the output directory with `HARNESSMESH_VIDEO_DIR=/tmp/harnessmesh-video`.

The recording covers:

1. CLI startup and agent discovery
2. tenant connection in the console
3. monitoring and usage context
4. approval detail and audit history
5. visual agent orchestrator
6. deterministic Simulation Lab pass

The demo data is intentionally mocked and contains no real credentials or
provider calls. Use the production operations runbook for a real deployment
recording.

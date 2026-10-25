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

For separate recordings, use:

```bash
cd frontend
npm run demo:videos
```

This creates `artifacts/videos/harnessmesh-cli-walkthrough.webm` for the CLI
workflow and `artifacts/videos/harnessmesh-ui-walkthrough.webm` for the React
console workflow.

The reference video is committed at `artifacts/videos/harnessmesh-walkthrough.webm`.
The repository README embeds the GitHub-hosted video asset and keeps the
committed poster/WebM as fallbacks. GitHub-hosted attachments are required for
reliable inline playback in README files; relative repository HTML `<video>`
elements are not rendered consistently. Running the command regenerates and
overwrites the local artifact; a new GitHub attachment must be created when the
published video is replaced.
Override the output directory with `HARNESSMESH_VIDEO_DIR=/tmp/harnessmesh-video`.

The combined recording covers:

1. CLI startup and agent discovery
2. tenant connection in the console
3. monitoring and usage context
4. approval detail and audit history
5. visual agent orchestrator
6. deterministic Simulation Lab pass

The separate recordings make the two perspectives explicit: the CLI recording
shows control-plane commands, while the UI recording shows what an operator
can inspect and control after the session is connected.

The demo data is intentionally mocked and contains no real credentials or
provider calls. Use the production operations runbook for a real deployment
recording.

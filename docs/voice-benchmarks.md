# Voice benchmark record

This file is the benchmark record for the pinned `light-1` profile. Values are
measurements, not guarantees. The checked-out development environment used for
this implementation has no usable Apple Command Line Tools/compiler, so native
model timings and accuracy are intentionally marked **not measured** rather than
invented.

## Warm-session acceptance table

| Metric | Target | Current development record | Measurement command |
| --- | ---: | --- | --- |
| Helper startup after models are warm | one process | Not measured | `ATCODE_VOICE_REAL_MODEL_TESTS=1 ...` |
| Model reload per utterance | 0 | Design: 0 | inspect helper stderr / metric events |
| TUI status visibility | ≤1 frame | Go event path covered by tests | `go test ./internal/tui -run TestVoice` |
| Capture after 2 s hold | ≤100 ms | Not measured | native device benchmark |
| First ASR partial | ≤2.5 s | Not measured | multilingual fixture runner |
| ASR final after endpoint | ≤500 ms | Not measured | multilingual fixture runner |
| TTS first audible sample, accelerated | ≤500 ms | Not measured | native playback benchmark |
| TTS first audible sample, CPU | ≤1.5 s | Not measured | native playback benchmark |
| RSS / VRAM | record actual value | Not measured | `/voice doctor` + OS profiler |
| Language detection accuracy | record per fixture | Not measured | `testdata/voice/fixtures.json` |

## Required run

Use a clean user voice directory and run the three streaming windows 1.5, 2,
and 3 seconds by setting `ATCODE_VOICE_ASR_CHUNK_SECONDS`. The helper accepts
only those three values and currently defaults to 2 seconds as the conservative
latency/stability choice; select a different default only after measuring it.
Record the fastest setting that does not duplicate or regress
partial text. Record cold install duration, helper startup, model load, first
partial, endpoint finalization, TTS first sample, real-time factor, RSS, VRAM,
and a per-language confusion table for English, Hindi, Arabic, Spanish, French,
German, Japanese, Korean, Mandarin, and the code-switch fixture.

Real-model runs are opt-in with `ATCODE_VOICE_REAL_MODEL_TESTS=1`; normal CI
must not download the pack. If a target is missed, add the hardware, backend,
model hashes, exact command, measured value, cause, and smallest follow-up here.

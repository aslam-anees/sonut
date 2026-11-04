# Local voice

`/voice` adds a temporary, local-first voice surface around the existing @code
agent. The Go TUI remains the only agent: voice requests route explicitly to
`agent.RunModeQuick`, or wait for confirmation before entering the existing
`agent.RunModeBuilder` workflow.

## Quickstart

1. Install a release of @code that includes the platform `atcode-voice` helper.
2. Start @code and run `/voice`. This opens the dedicated Voice workspace with
   Settings, Setup, Devices, and Doctor tabs; it does not add a status card to
   the chat transcript.
3. Open the Setup tab and press `s` to install the pack. The installer is
   resumable and writes to `~/.atcode/voice/`.
4. Hold Space for two seconds while the primary composer is focused. A normal
   short Space press inserts one space.
5. Speak. Three seconds of silence finalizes and submits the utterance.
6. Use `/voice status` or `/voice doctor` to reopen the relevant workspace tab.

The pack is optional; text input continues to work when the helper or models
are absent. `ATCODE_VOICE_DISABLE=1` disables startup and voice activation.
Setup can be cancelled with `/voice stop`; partial artifacts remain resumable
and are never promoted until their size and SHA-256 checks pass.

## Installed pack

The versioned `light-1` profile uses the pinned audio.cpp revision
`238ab6a9e321c17de8e120559f57efeedaeb1345` and stores models under
`~/.atcode/voice/models/`. The free-disk preflight is 1,450,000,000 bytes.

| Artifact | Bytes | SHA-256 | License |
| --- | ---: | --- | --- |
| Qwen3-ASR 0.6B Q8_0 GGUF | 1,151,272,416 | `6c44ec2fb4cee513892d7863c1fcc3ea6b699ffa4d899b0ef4ab19956d9544f7` | Apache-2.0; see Qwen model card |
| Supertonic 3 F16 GGUF | 255,637,890 | `bc9b5fa3b05015d3a0216005aa52484f4167c8830a65b6e66aeb84e23cc5d811` | OpenRAIL; see Supertone model card |
| Silero VAD 16 kHz weights | 1,239,748 | `c59271c284ae9c8335d795d60e0bfdb71aaaceec578d9bd9ffc1b8153c319ea1` | MIT |

The GGUF files are fetched from an immutable Hugging Face commit. The VAD
weights are fetched from the immutable audio.cpp commit. Every artifact is
HTTPS-only, size checked, SHA-256 checked, downloaded to `.part`, and renamed
atomically. A per-install lock prevents concurrent corruption. See
`native/voice/LICENSES.md` before redistributing a pack.

## Languages and voices

Qwen3-ASR is used for automatic language detection and supports the multilingual
profile shipped by audio.cpp, including English, Hindi, Arabic, Spanish,
French, German, Japanese, Korean, Mandarin Chinese, Cantonese, Portuguese,
Russian, Italian, Dutch, Turkish, Vietnamese, Thai, Indonesian, Malay, and
several additional language codes. Set `/voice language <code>` to provide a
hint; `auto` is the default. A hint is not silently changed into a detected
language.

Supertonic 3 supports: `en`, `ko`, `ja`, `ar`, `bg`, `cs`, `da`, `de`, `el`,
`es`, `et`, `fi`, `fr`, `hi`, `hr`, `hu`, `id`, `it`, `lt`, `lv`, `nl`, `pl`,
`pt`, `ro`, `ru`, `sk`, `sl`, `sv`, `tr`, `uk`, and `vi`. The default female
voice is `F1`; `F1` through `F5` are accepted. `/voice speed 0.5..2.0`
changes the speaking rate at synthesis time and never modifies stored audio.

If Supertonic cannot speak the response language, @code keeps the complete
response in the TUI and displays `TTS not available for this language`. The
light profile does not invoke a shell or download an OS voice. An OS-native TTS
adapter may be added later as an explicit, opt-in backend.

## Configuration

Voice settings are personal and are written only to the user config:

```json
{
  "voice": {
    "enabled": true,
    "language": "auto",
    "responseVoice": "F1",
    "speed": 1.05,
    "shortcut": "hold:space:2s",
    "inputDevice": "default",
    "outputDevice": "default",
    "modelProfile": "light",
    "silenceMs": 3000
  }
}
```

Useful commands:

```text
/voice
/voice on|off|status|setup|listen|stop|doctor|models
/voice language auto|en|hi|ar
/voice voice F1|F2|F3|F4|F5
/voice speed 0.5..2.0
/voice shortcut hold:space:2s|ctrl+space|alt+v|f8|off
/voice input [device-id]
/voice output [device-id]
```

Inside the Voice workspace, `↑`/`↓` selects a setting and `Enter` changes it;
`e` toggles voice, `l` cycles common recognition languages, `v` cycles female
voices F1–F5, `-`/`+` changes speed, `k` cycles shortcuts, `d` opens devices,
and `s` or `Enter` starts setup. `Tab` switches tabs, the top tabs are
clickable, and `Esc` or `q` returns to the normal @code screen. Typing `/`
also returns to the composer and starts a new slash command immediately.

Device listings use stable session-local IDs such as `default`, `input-0`, and
`output-0`; hardware names are supplied by miniaudio and IDs should be selected
from the current `/voice input` or `/voice output` listing.

Project config cannot override these device, voice, language, or shortcut
settings. Writes use the existing atomic config writer.

## Safety and interaction

Voice mode is temporary. Typing a printable character, Backspace, Delete,
paste, or another composer-editing key immediately cancels capture, queued TTS,
and the active utterance; the same keystroke is then processed normally.
Stable text already committed to the composer remains, while the unstable
partial span is removed. Esc cancels listening, speech, or a pending build
confirmation. Activating the shortcut during speech is barge-in.

Voice requests always carry explicit run metadata. A Quick-routed question can
run immediately and receives concise speakable response text. A Builder-routed
request is shown in the TUI and spoken as a yes/no confirmation. Ambiguous
answers fail closed. Approval submits the original transcript once to the
existing Builder mode; `/build`'s application-builder overlay is never toggled.

## Terminal and microphone permissions

Bubble Tea v2 keyboard enhancements are requested so key press, repeat, and
release events can be distinguished. On terminals or multiplexers without
release reporting, a genuine repeated Space is required for activation; one
ordinary Space is inserted immediately. `/voice doctor` reports the active
shortcut and helper state.

- macOS: System Settings → Privacy & Security → Microphone; allow the terminal
  application running @code, then restart it.
- Windows: Settings → Privacy & security → Microphone; enable microphone access
  and desktop-app access for the terminal.
- Linux: grant the terminal access to PipeWire/PulseAudio/ALSA, check the
  selected default source and sink, and retry `/voice input` or `/voice output`.

No raw microphone audio is written to disk by default. Transcript and audio
diagnostic logging are not enabled by this feature. Native diagnostics go to
stderr; the helper's stdout is reserved for protocol messages.

## Native builds and updates

`native/voice/CMakeLists.txt` fetches audio.cpp by the pinned commit, selects
only `qwen3_asr` and `supertonic`, and links the bundled Silero VAD runtime.
miniaudio 0.11.22 is fetched by its pinned SHA-256. The helper is separate from
the CGO-free Go binary and keeps ASR/TTS sessions warm between utterances.

Build a host helper and checksum it with:

```sh
ATCODE_VOICE_GOOS=darwin ATCODE_VOICE_GOARCH=arm64 \
  scripts/build-voice-native.sh
```

Cross builds must provide a CMake toolchain through
`ATCODE_VOICE_CMAKE_ARGS`. Update `ATCODE_AUDIO_CPP_REVISION`, the GGUF/VAD
URLs, sizes, hashes, and the license review together; run the native protocol
tests and an opt-in real-model smoke test before publishing.

Release artifacts are named `atcode-voice-{darwin,linux,windows}-{arm64,amd64}`
(`.exe` on Windows). The supported matrix is:

| Platform | Automatic backend selection | Optional backend |
| --- | --- | --- |
| macOS arm64 | Metal when cached hardware profile has headroom | CPU fallback |
| macOS amd64 | CPU | — |
| Linux amd64/arm64 | CPU unless cached GPU profile has headroom | CUDA or Vulkan |
| Windows amd64 | CPU unless cached GPU profile has headroom | CUDA or Vulkan |

Automatic selection reuses `~/.atcode/hardware.json` and is conservative because
the main agent may already occupy the GPU. Set `ATCODE_VOICE_BACKEND=cpu`,
`metal`, `cuda`, or `vulkan` to make the choice explicit after benchmarking.

The release script only stages a helper when the separately built artifact is
provided through `ATCODE_VOICE_ARTIFACT_ROOT`; the ordinary Go build continues
to use `CGO_ENABLED=0`.

## Testing and benchmarks

Unit and fake-helper integration tests do not download models. Run:

```sh
go test ./internal/voice ./internal/config
go test ./internal/tui -run 'TestVoice' -count=1
go test -race ./internal/voice ./internal/config
ATCODE_VOICE_REAL_MODEL_TESTS=1 go test ./internal/voice -run RealModel
```

The multilingual fixture manifest is in
`testdata/voice/fixtures.json`. Real-model tests are opt-in because the default
pack is about 1.4 GB and fixtures may contain user-provided recordings.
Benchmark procedure and the honest environment-specific results are recorded
in `docs/voice-benchmarks.md`.

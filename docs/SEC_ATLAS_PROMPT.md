# `/sec` Security Atlas implementation prompt

Use this prompt when evolving the native terminal atlas. It describes the visual and trust contract together so visual polish never creates misleading security claims.

> Design and implement an enterprise-grade **Security Atlas** page inside the existing native `/sec` TUI. The result should feel as calm, intentional, and information-dense as a first-party Apple terminal experience: black canvas, deep electric-blue ASCII world map, restrained near-white typography, dark-gray dividers, and generous negative space. Do not build an HTML dashboard or introduce a second theme.
>
> Make the map the visual center. Draw continents as an original scalable terminal-native ASCII/Unicode mask, not an image. Animate observed routes with sparse moving particles rather than flashing the whole screen. Use green for allowed traffic, red for blocked or confirmed hostile events, amber for sensitive/needs-review events, and blue for local/neutral topology. Honor reduced-motion settings and provide a pause control.
>
> The visualization must be driven by real `/sec` telemetry. Start with existing guarded egress events, passive local interface inventory, and explicitly marked events from the opt-in Linux sensor when it is active. Show destination, tool/source, verdict, protocol, bytes, time, and evidence confidence when available. Never invent packets, attacks, locations, or reachability. Only place a destination geographically when trusted offline GeoIP data or a recognized cloud-region identifier supplies evidence; put everything else on an explicit **unlocated** rail with a `?` marker. Label the current visibility boundary: “Observed egress decisions; packet capture off.” The sensor does not decrypt HTTPS or capture packet payloads.
>
> Interaction: `/sec atlas` opens the page directly; Tab/arrow keys switch `/sec` pages; Up/Down selects a flow; `f` cycles All → Blocked → Sensitive; Space pauses/resumes animation; Enter opens concise evidence; `a` opens the grounded security advisor with the selected event as context. Keep all controls discoverable in the footer and usable over SSH at narrow widths.
>
> Architecture: keep collectors separate from rendering. Default operation is agentless, read-only, non-privileged, and performs no LAN probes, DNS lookups, online GeoIP calls, or packet sniffing. Any future PCAP/live-capture adapter must be an explicit opt-in, show the selected interface and filter before starting, request the minimum OS privilege, redact payloads by default, impose memory/rate limits, and preserve an append-only audit trail. Prefer Go standard-library interface discovery; use GoPacket only behind the optional capture boundary. Do not import a large packet-analysis application as a dependency.
>
> Enterprise quality: handle empty, quiet, busy, blocked, narrow-terminal, no-color, reduced-motion, stale-data, and permission-denied states. Bound event retention, avoid secret payload display, expose provenance and timestamp, use deterministic tests for route placement and filters, and never market an observation as an attack unless a detector provides supporting evidence. The page should answer at a glance: **What is communicating? Where is it going? Was it allowed? Why does it matter? What should I do next?**

Reference principles:

- LAN-Orangutan: multi-interface inventory and explicit active discovery.
- GoPacket: optional packet decoding/capture adapter, isolated from the default path.
- Termshark: keyboard-first packet exploration and flow filtering.
- Netcap: structured audit records rather than raw-payload-first storage.

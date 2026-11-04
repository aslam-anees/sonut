# `/sec` route insight

The route inventory is a clean-room integration informed by the local
`ref/godoxy` checkout (MIT). It does not copy GoDoxy source, add a GoDoxy
dependency, or run a proxy control plane.

`/sec` recognizes the route declarations that are useful during workspace
review:

- GoDoxy-compatible Compose labels such as `proxy.aliases`, indexed labels
  such as `proxy.#1.port` and `proxy.#1.scheme`, visible TLS/auth settings,
  and middleware names.
- GoDoxy-style YAML `routes:` maps with host, target, port, scheme, TLS,
  authentication, and middleware fields.
- Kubernetes `Ingress` host/path/backend mappings.

These become typed route records, route assets, `routes-to` edges, and
exposure evidence in the existing static report. `configured-public` means a
public-facing mapping is declared in local configuration; it does not prove
DNS resolution, listener availability, certificate validity, proxy health, or
internet reachability. The collector never opens a Docker socket, contacts a
proxy, resolves a hostname, or performs network probing. Collection is
bounded to 512 route records and the existing workspace scan limits.

The native view is available through `/sec routes` (or the `0` key inside
`/sec`). Select a route and press Enter for its visible policy controls,
source, evidence IDs, and the reachability caveat.

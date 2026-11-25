> ⚠️ **Early concept draft.** This KEP is an early-stage exploration. It is **incomplete and may be inaccurate**, its direction is unsettled, and it should not be relied upon for implementation or as a description of committed behaviour. Expect substantial change.

# KEP-2.5: Credential Model & Token Rotation

**Status:** Drafting (Not ready for consumption)
**Parent:** [vNext Roadmap](../README.md)

Communication between hub and spoke is entirely hub-initiated. The hub reads and writes to spoke clusters via cluster-gateway; the spoke never opens a connection to the hub. The spoke "responds" to hub requests by writing to its own local API, which the hub reads back through cluster-gateway. No persistent outbound connection from spoke to hub is required.

| Direction | Mechanism |
|---|---|
| Hub → spoke | cluster-gateway (kubectl proxy) |
| Spoke → hub | Spoke writes to local API; hub reads via cluster-gateway |

## Bootstrap — Spoke Registration

When a spoke is registered from the hub (using a kubeconfig provided by the operator):

1. Hub generates an **ECDH keypair** (P-256) for the spoke relationship
2. Both sides perform a key agreement to derive a **shared AES-256-GCM symmetric key** — neither side transmits this secret; both independently compute the same value from the keypair material
3. Hub stores the derived symmetric key and the spoke's public key in `vela-system/spoke-credential-<spoke-name>` on the hub
4. Hub writes its own public key to the spoke via the bootstrap kubeconfig as `vela-system/hub-credential`
5. Spoke derives the same shared symmetric key locally and stores it alongside the hub public key
6. The bootstrap kubeconfig is discarded after registration — all subsequent communication uses cluster-gateway with the registered credential

The shared AES-256-GCM symmetric key is the **long-lived bootstrap credential**. It persists across token rotation cycles and is only refreshed via an explicit re-registration flow (see Bootstrap Credential Refresh below).

## Token Rotation Protocol

The hub drives all token rotation. A request/response Secret on the spoke serves as the message bus — the hub writes a request, the spoke updates it with a response, the hub reads it back via cluster-gateway.

**Step 1 — Hub initiates:**
1. Hub generates a fresh **RSA-OAEP keypair** (2048-bit) for this rotation cycle
2. Hub constructs: `{ newPublicKey: <RSA public key>, requestId: <uuid>, issuedAt: <timestamp> }`
3. Hub encrypts the payload with the **shared AES-256-GCM symmetric key**
4. Hub writes the encrypted payload to the spoke: `vela-system/token-rotation-<requestId>` labelled `oam.dev/hub-request: token-rotation`

**Step 2 — Spoke processes:**
1. Spoke watches for Secrets labelled `oam.dev/hub-request: token-rotation` on its local API
2. Spoke decrypts the payload using its copy of the shared AES-256-GCM symmetric key
3. Spoke generates or rotates the cluster-gateway access token
4. Spoke encrypts the new token with the hub's **new RSA public key** from the payload — the token is now only readable by the holder of the matching private key
5. Spoke writes `{ encryptedToken: <RSA-encrypted token>, requestId: <uuid>, respondedAt: <timestamp> }` back to the same Secret

**Step 3 — Hub collects:**
1. Hub watches the request Secret via cluster-gateway, detects response fields present
2. Hub decrypts the token with its **new RSA private key**
3. Hub stores the new token in `vela-system/spoke-credential-<spoke-name>`
4. Hub deletes the request Secret from the spoke
5. Hub logs the rotation event for audit

The fresh RSA keypair per rotation cycle provides **forward secrecy on token delivery** — compromise of one cycle's private key cannot expose tokens from any other cycle.

## Rotation Has a Circular Dependency

The rotation request travels to the spoke **through cluster-gateway**, which needs a currently valid credential to reach it. So the channel that repairs an expired credential is the same channel the expired credential gates.

A single failed rotation is harmless, since the old token is still valid and the next attempt succeeds. Sustained failure is not: once the credential expires, there is no in-band way to deliver a new one, and recovery means manual re-registration against every affected spoke. That turns a transient outage on the rotation path into an operational incident proportional to fleet size.

This needs designing for rather than discovering. Rotation must begin far enough before expiry that the retry budget exceeds any plausible outage, and the alert has to fire on *rotation failing*, not on the credential having expired. By then the channel is gone.

---

## Secret Lifecycle & Audit

Request Secrets are deleted from the spoke immediately after the hub reads and stores the response. The hub-side audit trail is maintained via structured controller logs:

```
INFO  token-rotation  spoke=prod-eu requestId=abc123 status=initiated
INFO  token-rotation  spoke=prod-eu requestId=abc123 status=response-received respondedAt=...
INFO  token-rotation  spoke=prod-eu requestId=abc123 status=complete secret-deleted=true
```

## Bootstrap Credential Refresh

If the long-lived shared symmetric key needs manual rotation the operator triggers a re-registration flow — providing a fresh kubeconfig, repeating the ECDH key agreement, and overwriting the symmetric key on both sides. Existing token rotation resumes using the new symmetric key on the next scheduled cycle.

## Cryptographic Primitives

| Purpose | Algorithm | Rationale |
|---|---|---|
| Bootstrap key agreement | ECDH (P-256) | Standard, no key transmission required |
| Request/response envelope | AES-256-GCM | Fast authenticated symmetric encryption |
| Token delivery | RSA-OAEP (2048-bit) | Token readable only by hub even if symmetric key is later compromised |
| Key material storage | Kubernetes Secret | Standard; can be KMS-backed (see Open Questions) |

## Dispatch Integrity: Hub Signing

Every `Component` the hub dispatches is signed. vela-agent verifies the signature before it applies anything, and refuses on failure: there is no fail-open mode. Signing gives provenance; it is separate from keeping the payload private ([KEP-2.1](../2.1-core-api/README.md#payload)).

### What is signed

The Component's namespace and name, and the whole `Component.spec`: target cluster, revision, tombstone flag, resource index with per-resource sha256, apply options, health, status paths, payload reference and payload sha256. The signature and its key ID sit in `spec.signature`, outside what they cover.

The signed bytes are canonical JSON as the API server hands an object back (keys sorted, whole numbers as integers, null fields dropped), so a Component still verifies after a round trip through the API server rewrites its raw fields. Namespace and name are covered so a valid Component copied elsewhere in the same cluster does not verify.

| Attack | Defence |
|---|---|
| Create a Component directly | Unsigned or badly signed Components are refused. Admission also refuses writes from anyone but the configured producer service accounts (vela-core's by default) |
| Keep a valid payload, flip a plaintext field (e.g. `takeOver: true`) | The whole spec is signed |
| Copy a valid Component into another namespace or under another name | Namespace and name are signed |
| Swap or edit the payload Secret | Its sha256 is in the signed spec; the controller re-hashes what it reads |
| Replay a Component signed for another cluster | `spec.target.cluster` is signed; the controller refuses anything not addressed to it |
| Replay an older Component (forced rollback) | `spec.revision` is signed and monotonic; the controller refuses anything lower than it last applied |
| Delete a Component to garbage-collect what it manages | Resources are collected only on a signed tombstone ([KEP-2.2](../2.2-spoke-controller/README.md#deletion)) |

**What signing does not cover.** A compromised hub or signing key. And anyone who may legitimately create Applications on the hub: they get whatever they render, correctly signed. Spoke-side guardrails ([KEP-2.3](../2.3-hub-controller/README.md)) are what bound that. The signature proves *where* a request came from, not that it is *allowed*.

### The signing key

An Ed25519 key pair, generated by vela-core into a Secret in `vela-system` on first start. Each key has an ID, carried on every Component it signs.

### Sealing keys

Some values vela-agent reports back must not be readable by whoever can read a Component: a `$(component.<name>)` read of a Secret's data, or any path a definition marks `+sensitive` ([KEP-2.2](../2.2-spoke-controller/README.md#observations)). vela-agent seals those to the hub.

Each signing key has a sibling X25519 **sealing key**, derived from the signing seed with HKDF-SHA256 under its own label. The hub holds no second secret, and the two keys are never the same key used two ways. The public half travels in the trust Secret beside the signing key, as `<key ID>.seal`, and is pushed, rotated and retired with it.

vela-agent seals each sensitive value to the sealing key of the key that signed the Component (`spec.signature.keyID`), as an anonymous NaCl box, and records that key ID in `status.sealedWith`. Only the hub can open it. A key is retired only once no Component carries its ID, so the hub can always open what was sealed to it.

Sealing gives confidentiality, not authenticity: vela-agent has no key of its own, so the hub cannot prove which agent wrote a value, as for every other status field.

### Trust: the hub keeps it pushed

Each vela-agent trusts a list of hub public keys (and their sealing keys), held in the Secret `vela-component-trust` in `vela-system` in its own cluster. The hub **continuously reconciles** that list onto every joined cluster: it writes the current keys, reads them back each pass, and reverts any drift, raising an event and a condition on the cluster when it finds a key it did not issue.

Trust therefore rests on write access to `vela-system` in the target cluster. Anyone with that access can already change vela-agent itself, so the list is no weaker than what it protects. Under pull delivery, where the hub cannot write to the spoke, the list travels the same way Components do.

### Rotation

On request or on a schedule, with an overlap window:

1. An operator annotates `vela-component-signing-keys` with `component.oam.dev/rotate-signing-key`. On its next pass the trust push generates a new key, makes it the active one, removes the annotation and pushes both keys everywhere.
2. The application controller watches the signing keys Secret and, once another key is active, reconciles every dispatched Application, which re-signs its Components with the new key at the same revision, since the spec is unchanged.
3. On a later pass, a key that is not active and that no Component on any cluster is signed with, or has sealed values to (`status.sealedWith`), is retired: deleted from the signing keys and dropped from every cluster's trust. A cluster the hub cannot list holds every key in use.

With `--component-signing-key-max-age` set (off by default), the trust push sets the same annotation itself once the active key has been active for longer, going by `component.oam.dev/active-since` on the signing keys Secret. A Secret without that annotation starts its clock when first seen, so turning the schedule on never rotates at once. While a cluster cannot be listed, scheduled rotations add keys that none retire until it can.

The hub holds the active key and, during a rotation, the one it replaces. When it holds more, an earlier rotation has not retired its key: the trust push records a `SigningKeysPilingUp` warning on the signing keys Secret, naming each old key and the Components that carry it (or the cluster it could not list), and reports `kubevela_component_signing_keys` and `kubevela_component_signing_key_age_seconds`.

The keys the hub retired or revoked, until every cluster has dropped them, are listed in `component.oam.dev/removed-keys` on the signing keys Secret, so their leaving a cluster's trust is never reported as foreign, whichever replica pushes next.

### Revocation

A key that may have leaked cannot wait for its Components to move off it. An operator annotates `vela-component-signing-keys` with `component.oam.dev/revoke-signing-key: <key ID>`, and on its next pass the trust push:

1. makes a new key active first, if the revoked key is the active one;
2. deletes the revoked key, whatever still carries it, removes the annotation, and pushes, so no cluster trusts the key from then on;
3. and, as a key has left the Secret, the application controller reconciles every dispatched Application, which re-signs its Components with the active key.

Until a Component is re-signed, vela-agent refuses its spec (`UntrustedKey`) and leaves its applied resources as they are. Values sealed to the revoked key cannot be opened by the hub until vela-agent observes them again and seals them to the new one. A request naming a key the hub does not hold is spent, and recorded as a `RevokeSigningKeyFailed` warning.

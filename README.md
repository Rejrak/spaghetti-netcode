# authServer-netcode

This repository contains a TCP server prototype based on Protocol Buffers and an actor-based concurrency model.  
The server processes authorization requests, queries a local SQLite attribute store (synchronized from Keycloak), and evaluates policy decisions using both static attributes and optional dynamic backends (e.g., Cosmos LCD or HTTP policy evaluators).  
The code accompanies the scientific paper for reproducibility and experimental validation.

---

## Project Structure

The Go module declared in `go.mod` is:

- `module authServer`

Directory overview:

- `cmd/`
  - `main.go` — server entrypoint
- `internal/actors/server/`
  - TCP session actors, synchronization actor, dynamic policy evaluator orchestration
- `internal/actors/synchronizer/`
  - periodically pulls user attributes from Keycloak into SQLite
- `internal/pkg/packets/`
  - generated Protobuf bindings (`packets.pb.go`) and framing utilities
- `internal/storage/sqlite/`
  - SQLite repository (`authblock.db`)
- `internal/remote/`
  - external backends
  - `remote/keycloak/` — Keycloak integration
  - `remote/policy/` — HTTP dynamic policy evaluator + optional Cosmos LCD integration
- `internal/user/`
  - roles, permissions, CRUD checks, and attribute logic
- `shared/`
  - shared protocol definitions (`packets.proto`)

---

## Network Protocol (TCP + Protobuf)

The server listens on a TCP socket (default `:6000`) and exchanges Protobuf messages defined in:

shared/packets.proto

### Protobuf Definitions

```proto
syntax = "proto3";

package packets;

option go_package = "pkg/packets";

message AuthMessage {
    string address = 1;
    string operation = 2;
}

message ResponseMessage {
    bool success = 1;
    string message = 2;
}

message CosmosPacket {
    string request_id = 1;
    oneof msg {
        AuthMessage authMessage = 2;
        ResponseMessage responseMessage = 3;
    }
}
```

#### Message Semantics

- **AuthMessage**
  - `address`: user identifier (e.g., wallet address, user id, etc.)
  - `operation`: requested action (e.g., `send`, `mint`, `admin:update`)

- **ResponseMessage**
  - `success`: boolean authorization outcome
  - `message`: descriptive feedback or error reason

- **CosmosPacket**
  - encapsulates either an `AuthMessage` request **or** a `ResponseMessage` reply
  - `request_id` must be echoed back unchanged in server responses


### TCP Framing

Messages are framed over TCP using a simple length-prefixed binary format:

1. Serialize the Protobuf `CosmosPacket` payload into bytes
2. Compute the payload length `N`
3. Send a 4-byte **big-endian** unsigned integer containing `N`
4. Send the raw Protobuf payload

Clients must mirror this procedure when reading responses, first parsing the prefix and then the payload.

---

## Processing Pipeline

The authorization workflow follows four main stages:

1. **Connection**
   - Client establishes a persistent TCP session to the server (default `:6000`)

2. **Request**
   - Client sends a `CosmosPacket` containing:
     - `request_id`
     - `AuthMessage{ address, operation }`

3. **Evaluation**
   - Server extracts user attributes from the local SQLite store
   - Authorization may combine:
     - roles and permissions
     - CRUD semantics
     - dynamic HTTP policy evaluator
     - optional Cosmos LCD queries (e.g., balance inspection)
     - local static attribute checks

4. **Response**
   - Server sends a `CosmosPacket` containing:
     - same `request_id`
     - `ResponseMessage{ success, message }`

---

## User Attribute Synchronization

User attributes originate from a Keycloak realm and are periodically synchronized to a local SQLite database using a dedicated actor.  
This component supports batching, staleness thresholds, and timeout controls.

Key configuration fields:

```go
type Config struct {
    DBPath string

    PollInterval time.Duration
    StaleAfter   time.Duration
    MaxBatch     int

    RemoteTimeout time.Duration

    KeycloakBaseURL             string
    KeycloakRealm               string
    KeycloakClientID            string
    KeycloakClientSecret        string
    KeycloakWalletAttributeName string
    KeycloakEnableWalletLookup  bool
}
```


These values may vary depending on experimental configurations or production deployments.

---

## Requirements

### Software Dependencies

- Go ≥ 1.22 (repository targets `go 1.24`)
- `protoc` ≥ 3.x
- Go Protocol Buffers plugin:
  - `google.golang.org/protobuf`

### Optional Dependencies

Required only if dynamic policy evaluation is enabled:

- Keycloak instance (user attribute provisioning)
- HTTP policy evaluation backend
- Cosmos LCD endpoint (optional on-chain lookups)

---

## Building the Server

From the repository root:

```bash
go mod download
go build -o build/authServer ./cmd
```

The resulting binary will be `build/authServer`.

---

## Running the Server

To run the server locally:

```bash
./build/authServer -listenaddr :6000
```

Runtime assumptions:

- external services (Keycloak, HTTP policy evaluator, Cosmos LCD) must be reachable if enabled
- Cosmos LCD integration is optional and only used for dynamic authorization scenarios

---


## Minimal Go Client Example

```go
conn, _ := net.Dial("tcp", "localhost:6000")
defer conn.Close()

pkt := &packets.CosmosPacket{
    RequestId: "req-123",
    Msg: &packets.CosmosPacket_AuthMessage{
        AuthMessage: &packets.AuthMessage{
            Address:   "user-address-1",
            Operation: "send",
        },
    },
}

data, _ := packets.CosmosPacketToBytes(pkt)
conn.Write(data)
// client must read the 4-byte length prefix and then the Protobuf payload
```

## Compiling Protobuf Definitions

The repository ships with a shared `.proto` file used for both server and client implementations:


The Go server uses generated bindings, while clients in other languages can compile the same specification using language-specific plugins.

---

### Server-Side Bindings (Go)

**Source:**
- `shared/packets.proto`

**Generated output:**
- `internal/pkg/packets/packets.pb.go`

#### Step 1 - Install Protobuf Compiler

Example (macOS):

```bash
brew install protobuf
protoc --version
```

#### Step 2 - Install Go Plugin
```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
export PATH="$PATH:$(go env GOPATH)/bin"
```

Verify plugin installation:
```bash
which protoc-gen-go
```

#### Step 3 - Generate Bindings
From the repository root:
```bash
protoc \
  -I=shared \
  --go_out=internal/pkg/packets \
  --go_opt=paths=source_relative \
  shared/packets.proto
```
This produces (or updates):
```bash
internal/pkg/packets/packets.pb.go
```
### Client Bindings (Other Languages)
Bindings can be generated for any language supported by protoc.

#### Python
```bash
protoc \
  -I=shared \
  --python_out=client/python \
  shared/packets.proto
```
#### JavaScript (Node):
```bash
protoc \
  -I=shared \
  --js_out=import_style=commonjs,binary:client/js \
  shared/packets.proto
```

### TCP Framing Requirement
All clients must implement the same TCP framing convention expected by the server:
```bash
4-byte big-endian length prefix + Protobuf payload
```

## Summary

To interact with the authorization server:

1. Compile the Protobuf specification for both server and client implementations
2. Build the TCP authorization server
3. Run the server and establish a TCP connection from a client
4. Send `CosmosPacket{ AuthMessage }` requests
5. Receive `CosmosPacket{ ResponseMessage }` replies
6. Inspect the `success` and `message` fields to determine the authorization outcome

This repository acts as an implementation artifact intended to support:

- reproducibility
- benchmarking
- experimental evaluation
- artifact verification

in the context of the associated scientific publication.

---

## Artifact Evaluation & Reproducibility Notes

For artifact evaluators or researchers attempting to reproduce experimental results:

- No external dependencies are required for static authorization experiments if a prepopulated `authblock.db` is supplied.
- Optional Keycloak synchronization enables dynamic user attribute provisioning.
- Optional HTTP policy evaluation and Cosmos LCD integration enable experimentation with richer policy models.
- The TCP protocol and Protobuf specification are intentionally compact to minimize implementation overhead for client tooling.
- The codebase aims to be deterministic aside from external query latency (Keycloak / HTTP / LCD).

---

## Experimental Configuration (Optional)

If experiments require dynamic policy evaluation:

- Ensure a working Keycloak instance is available
- Configure credentials inside the server (see `Config` structure)
- Deploy policy evaluation backend if applicable
- Optionally connect to a Cosmos LCD endpoint for balance queries

These components are strictly optional and are not necessary for baseline authorization experiments.

---

## License & Academic Use

This implementation is intended for academic and research use.  
Please cite the associated paper if this artifact is used in derived work, benchmarking, or experimental comparisons.

---

## Contact & Issues

For questions, implementations in other languages, or bug reports:

- Contributions and pull requests are welcome
- Client implementations (Go, Python, JS) may be extended based on request

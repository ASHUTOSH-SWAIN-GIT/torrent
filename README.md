# BitTorrent Client

A fully-featured BitTorrent client written in Go that implements the BitTorrent protocol for downloading and sharing files over peer-to-peer networks.

## Features

- **Multi-tracker Support**: Queries multiple trackers concurrently to maximize peer discovery
- **Concurrent Downloads**: Downloads pieces from multiple peers simultaneously for faster speeds
- **Block Pipelining**: Requests multiple blocks in parallel to optimize throughput
- **Upload Support**: Automatically uploads downloaded pieces to other peers (tit-for-tat)
- **Dynamic Peer Management**: Automatically re-announces to trackers to maintain optimal peer count
- **Message Dispatcher**: Single-threaded message reading with concurrent handlers to prevent read conflicts
- **Out-of-Order Block Handling**: Efficiently handles out-of-order block arrivals during pipelining
- **SHA-1 Verification**: Validates each piece's integrity before writing to disk
- **Connection Resilience**: Automatically handles peer disconnections and retries

## Architecture

### High-Level Flow

```
1. Parse .torrent file
   ↓
2. Query all trackers concurrently
   ↓
3. Connect to multiple peers (up to 50)
   ↓
4. Download pieces concurrently from peers
   ↓
5. Verify piece integrity (SHA-1)
   ↓
6. Write verified pieces to disk
   ↓
7. Upload pieces to other peers (tit-for-tat)
```

### Key Components

#### 1. **Tracker Package** (`internal/tracker/`)
- Parses `.torrent` files (Bencode format)
- Queries HTTP and UDP trackers
- Handles multiple tracker tiers
- Deduplicates peers from multiple trackers

#### 2. **Peer Package** (`internal/peer/`)
- **MessageDispatcher**: Single reader that routes messages to appropriate handlers
  - Prevents concurrent read conflicts
  - Routes `Piece` messages to download handlers
  - Routes `Request` messages to upload handlers
  - Tracks choke/unchoke state
- **Downloader**: Implements piece and block downloading
  - Block pipelining (requests multiple blocks in parallel)
  - Out-of-order block handling via `PieceBuffer`
  - Handles choke/unchoke states
- **Message Handling**: BitTorrent protocol messages
  - Handshake, Interested, Request, Piece, Have, Choke/Unchoke
  - Upload handling (serves pieces to requesting peers)

#### 3. **Downloader Package** (`internal/downloader/`)
- **Session Management**: Main download orchestration
- **Worker Pool**: Manages concurrent peer connections
- **Dynamic Re-announcing**: Periodically queries trackers for fresh peers
- **Piece Queue**: Manages which pieces to download next
- **Progress Tracking**: Monitors download progress and active connections

#### 4. **Bencode Package** (`internal/bencode/`)
- Parses and encodes Bencode format (used in `.torrent` files)

## How It Works

### 1. Torrent File Parsing

The client reads the `.torrent` file and extracts:
- **Info Hash**: SHA-1 hash of the `info` dictionary (identifies the torrent)
- **Piece Hashes**: SHA-1 hashes for each piece (for verification)
- **Piece Length**: Size of each piece (except the last one)
- **Trackers**: List of tracker URLs (HTTP and UDP)
- **File Name**: Name of the file to download

### 2. Tracker Communication

- Queries all trackers **concurrently** for maximum speed
- Supports both HTTP and UDP trackers
- Combines peer lists from all successful trackers
- Deduplicates peers (same IP:port)
- Re-announces periodically to maintain peer count (target: 20+ peers)

### 3. Peer Connection

- Attempts to connect to up to **50 peers** simultaneously
- Performs BitTorrent handshake:
  - Sends protocol identifier, info hash, and peer ID
  - Verifies peer's response
- Sends `Interested` message to indicate we want to download
- Waits for peer to send `Unchoke` message

### 4. Piece Downloading

Each worker connection:
1. **Selects a piece** from the queue
2. **Checks if peer has the piece** (via bitfield)
3. **Downloads using pipelining**:
   - Requests multiple blocks (default: 5) in parallel
   - Blocks can arrive out of order
   - `PieceBuffer` handles out-of-order assembly
4. **Verifies piece integrity** using SHA-1 hash
5. **Writes to disk** at the correct offset
6. **Broadcasts `Have` message** to all connected peers

### 5. Block Pipelining

Instead of downloading blocks sequentially:
```
Old: Request Block 1 → Wait → Receive → Request Block 2 → Wait → Receive
New: Request Block 1, 2, 3, 4, 5 → Receive (any order) → Request Block 6 → ...
```

This significantly improves download speed by:
- Reducing idle time waiting for responses
- Better utilizing network bandwidth
- Handling network latency more efficiently

### 6. Upload Handling

Each connection runs an upload handler in the background:
1. Sends `Unchoke` to allow peer to request from us
2. Listens for `Request` messages from peer
3. Checks if we have the requested piece
4. Reads piece data from disk
5. Sends `Piece` message with the block data

This implements **tit-for-tat**: peers are more likely to unchoke you if you upload to them.

### 7. Message Dispatcher

The `MessageDispatcher` solves a critical problem: **only one goroutine can read from a `net.Conn` at a time**.

**Problem:**
- Download handler needs to read `Piece` messages
- Upload handler needs to read `Request` messages
- Both can't read from the same connection simultaneously

**Solution:**
- Single `readLoop` goroutine reads all messages
- Routes messages to appropriate channels:
  - `Piece` → `pieceCh` (for downloads)
  - `Request` → `requestCh` (for uploads)
  - `Choke/Unchoke` → `chokeCh` (for state tracking)
- Multiple handlers can read from channels concurrently

### 8. Dynamic Peer Management

The client maintains optimal peer count:
- **Target**: 20+ active peers
- **Re-announce intervals**:
  - Every 3 seconds if < 15 peers
  - Every 5 seconds if < 25 peers
  - Every 15 seconds if ≥ 25 peers
- Automatically connects to new peers as old ones disconnect
- Removes dead connections automatically

## Project Structure

```
torrent/
├── main.go                    # Entry point
├── go.mod                     # Go module definition
├── internal/
│   ├── bencode/
│   │   └── bencode.go        # Bencode parsing/encoding
│   ├── tracker/
│   │   ├── tracker.go        # Tracker communication (HTTP/UDP)
│   │   └── udp_tracker.go   # UDP tracker implementation
│   ├── peer/
│   │   ├── peer.go           # Peer connection and handshake
│   │   ├── message.go        # BitTorrent message types
│   │   ├── downloader.go    # Piece/block downloading logic
│   │   ├── dispatcher.go    # Message dispatcher (single reader)
│   │   ├── peer_picker.go   # Peer selection logic
│   │   └── state.go         # Peer state management
│   ├── downloader/
│   │   └── downloader.go    # Main download session management
│   └── torrent/
│       └── session.go       # Alternative session implementation
└── README.md                 # This file
```

## Technical Details

### BitTorrent Protocol Implementation

The client implements the following BitTorrent protocol messages:

- **Handshake**: Initial connection establishment
- **Interested**: Indicates we want to download
- **Not Interested**: Indicates we don't want to download
- **Choke**: Peer is not allowing us to download
- **Unchoke**: Peer is allowing us to download
- **Request**: Request a specific block (piece index + offset + length)
- **Piece**: Contains block data (piece index + offset + data)
- **Have**: Indicates peer has a specific piece
- **Bitfield**: Indicates which pieces peer has (sent after handshake)

### Configuration Constants

- **DefaultBlockSize**: 128 KB (optimal for most networks)
- **MaxPipelineRequests**: 5 (number of blocks to request in parallel)
- **BlockTimeout**: 8 seconds (timeout for block download)
- **MaxPeers**: 50 (maximum concurrent connections)
- **TargetPeers**: 20 (target number of active peers)

### Error Handling

- **Connection Errors**: Automatically retries with different peers
- **Piece Verification Failures**: Discards piece and requeues for another peer
- **Timeout Handling**: Blocks that timeout are retried on different peers
- **Dead Connection Detection**: Automatically removes dead connections

## Limitations & Future Improvements

- **Rarest-First Piece Selection**: Currently downloads pieces in order; rarest-first would improve swarm distribution
- **DHT Support**: No Distributed Hash Table support (relies only on trackers)
- **Magnet Links**: Only supports `.torrent` files, not magnet links
- **Multi-File Torrents**: Currently handles single-file torrents
- **Bandwidth Throttling**: No upload/download rate limiting
- **Resume Support**: Cannot resume interrupted downloads

## License

This project is for educational purposes. Use responsibly and in accordance with applicable laws and copyright regulations.

## Contributing

This is a learning project. Feel free to fork, modify, and experiment!

## Acknowledgments

Built as an educational implementation of the BitTorrent protocol specification.


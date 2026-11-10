// Dobos is one Go process. The daemon is the only running container; the rest
// are the stores it owns on the host. Its internal structure is modelled as
// components, because nothing inside it deploys on its own.

dobos = softwareSystem "Dobos" "Presents torrents as ordinary media files and streams their bytes on demand, without downloading whole files to disk." {

    daemon = container "Dobos Daemon" "Single Go binary: mounts the virtual library, runs the torrent engine and serves the control API." "Go 1.26, cgo FUSE" "Layer Runtime" {
        vfs = component "Virtual Filesystem" "Serves reads on stub files by asking the engine for the byte range. internal/vfs." "Go, libfuse3" "Layer Runtime"
        engine = component "Torrent Engine" "GoStorm on a vendored anacrolix/torrent: sessions, piece priority, read-ahead and the piece cache. internal/gostorm." "Go" "Layer Runtime"
        library = component "Library Manager" "Turns a torrent into named stub files, and replaces or removes them. internal/library." "Go" "Layer Runtime"
        control = component "Control API and Panel" "HTTP API and dashboard for configuration, library operations and health. main.go, internal/monitor." "Go net/http" "Layer Runtime,Web UI,Unauthenticated"
        warmup = component "Index Cache" "Keeps each file's head and index bytes on disk so a scan or a seek answers before the swarm does. internal/warmup." "Go" "Layer Runtime"
    }

    stubs = container "Stub Library" "Small files on the host, one per title, that name a torrent and a file inside it." "Files on disk" "Layer Data,Storage"
    state = container "State Stores" "Torrent database, metadata index and the on-disk index cache." "bbolt, SQLite, files" "Layer Data,Database"
    config = container "Configuration" "One JSON file with every setting. Holds tracker and API credentials." "config.json" "Layer Data,Storage"

    daemon.vfs -> daemon.engine "Requests byte ranges from" "In-process"
    daemon.vfs -> daemon.warmup "Reads head and index bytes from" "In-process"
    daemon.vfs -> stubs "Lists and resolves" "File I/O"
    daemon.library -> stubs "Writes, replaces and removes" "File I/O"
    daemon.library -> daemon.engine "Adds and drops torrents in" "In-process"
    daemon.control -> daemon.library "Runs library operations through" "In-process"
    daemon.control -> daemon.engine "Reads session and health state from" "HTTP on loopback"
    daemon.control -> config "Reads and writes" "File I/O"
    daemon.engine -> state "Stores torrents and metadata in" "File I/O"
    daemon.warmup -> state "Stores index bytes in" "File I/O"
    daemon.engine -> swarm "Exchanges pieces with peers and announces to trackers of" "BitTorrent, HTTP(S)/UDP" "Inbound across trust boundary"
}

mediaServer -> dobos.daemon.vfs "Scans and reads media files from" "FUSE mount"
operator -> dobos.daemon.control "Configures and monitors through" "HTTP"
automation -> dobos.daemon.control "Adds, replaces and removes titles through" "HTTP/JSON"
